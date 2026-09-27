package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
	"github.com/mrhollen/crowban/internal/infrastructure/pubsub"
	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

type sseEvent struct {
	ID    string
	Event string
	Data  string
}

func readSSEEvent(reader *bufio.Reader, timeout time.Duration) (sseEvent, error) {
	type result struct {
		ev  sseEvent
		err error
	}
	ch := make(chan result, 1)

	go func() {
		var ev sseEvent
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				ch <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if ev.Event != "" || ev.Data != "" || ev.ID != "" {
					ch <- result{ev: ev}
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue // ignore comments (: connected, : ping)
			}
			if strings.HasPrefix(line, "id: ") {
				ev.ID = strings.TrimPrefix(line, "id: ")
			} else if strings.HasPrefix(line, "event: ") {
				ev.Event = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				ev.Data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	select {
	case res := <-ch:
		return res.ev, res.err
	case <-time.After(timeout):
		return sseEvent{}, fmt.Errorf("timeout waiting for SSE event")
	}
}

func newRealtimeStack(t *testing.T) (stack, *pubsub.Broker, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-realtime-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
	now := clock.New()
	mint := idgen.New()

	app := NewApp(Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
		Broker:     broker,
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return stack{Handler: mux, App: app}, broker, srv
}

func TestEvents_SSE_ConnectAndReceiveTaskCreated(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)

	// Create project
	proj := createProject(t, stk, "Realtime Project")

	// Connect SSE client to GET /api/v1/projects/{id}/events
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect to SSE: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	reader := bufio.NewReader(res.Body)

	// Mutate: Create a task in the project
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"project_id": proj.ID,
		"title":      "Realtime Task 1",
		"actor":      actor("ada"),
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/tasks = %d (%s)", code, body)
	}
	createdTask := mustDecode[createTaskResponse](t, body).Task

	// Read event from SSE stream
	ev, err := readSSEEvent(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent: %v", err)
	}

	if ev.Event != "created" {
		t.Errorf("ev.Event = %q, want created", ev.Event)
	}
	var data realtimeEventDTO
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("unmarshal event data: %v (raw %q)", err, ev.Data)
	}
	if data.TaskID != createdTask.ID {
		t.Errorf("data.TaskID = %q, want %q", data.TaskID, createdTask.ID)
	}
	if data.Kind != "created" {
		t.Errorf("data.Kind = %q, want created", data.Kind)
	}
	if data.Status != "todo" {
		t.Errorf("data.Status = %q, want todo", data.Status)
	}
	if data.Timestamp == "" {
		t.Error("data.Timestamp is empty")
	}
}

func TestEvents_SSE_ReceiveTaskUpdatedAndClaimed(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)

	proj := createProject(t, stk, "Claim Lifecycle Project")
	task := newTask(t, stk, proj.ID, "Task to claim")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer res.Body.Close()

	reader := bufio.NewReader(res.Body)

	// Claim the task
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": map[string]any{"type": "agent", "name": "runner-1"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /tasks/%s/claim = %d (%s)", task.ID, code, body)
	}

	ev, err := readSSEEvent(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent (claim): %v", err)
	}
	if ev.Event != "claimed" {
		t.Errorf("ev.Event = %q, want claimed", ev.Event)
	}
	var data realtimeEventDTO
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("unmarshal claim event data: %v", err)
	}
	if data.TaskID != task.ID {
		t.Errorf("data.TaskID = %q, want %q", data.TaskID, task.ID)
	}
	if data.Status != "doing" {
		t.Errorf("data.Status = %q, want doing", data.Status)
	}
	if data.Kind != "claimed" {
		t.Errorf("data.Kind = %q, want claimed", data.Kind)
	}
}

func TestEvents_SSE_ProjectScoping(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)

	proj1 := createProject(t, stk, "Project One")
	proj2 := createProject(t, stk, "Project Two")

	// Client subscribes only to proj1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj1.ID+"/events", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)

	// Create task in proj2 (must NOT be received by proj1 subscriber)
	newTask(t, stk, proj2.ID, "Proj 2 Task")

	// Create task in proj1 (must be received)
	task1 := newTask(t, stk, proj1.ID, "Proj 1 Task")

	ev, err := readSSEEvent(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent: %v", err)
	}
	var data realtimeEventDTO
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.TaskID != task1.ID {
		t.Errorf("received task %q, want task %q (project scoping failed!)", data.TaskID, task1.ID)
	}
}

func TestEvents_SSE_QueryParamEndpoint(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)

	proj := createProject(t, stk, "Query Param Project")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect via /api/v1/events?project_id=...
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events?project_id="+proj.ID, nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)

	task := newTask(t, stk, proj.ID, "Query Param Task")

	ev, err := readSSEEvent(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent: %v", err)
	}
	var data realtimeEventDTO
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.TaskID != task.ID {
		t.Errorf("TaskID = %q, want %q", data.TaskID, task.ID)
	}

	// Calling /api/v1/events without project_id returns 400
	badReq, _ := http.NewRequest("GET", srv.URL+"/api/v1/events", nil)
	badRes, err := srv.Client().Do(badReq)
	if err != nil {
		t.Fatalf("do badReq: %v", err)
	}
	defer badRes.Body.Close()
	if badRes.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing project_id", badRes.StatusCode)
	}
}

func TestEvents_SSE_UnknownProjectReturns404(t *testing.T) {
	_, _, srv := newRealtimeStack(t)

	res, err := srv.Client().Get(srv.URL + "/api/v1/projects/01JNONEXISTENTPROJECT0000000/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown project", res.StatusCode)
	}
}

func TestEvents_SSE_ClientDisconnectCleanup(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)

	proj := createProject(t, stk, "Disconnect Project")

	ctx, cancel := context.WithCancel(context.Background())

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}

	// Verify subscriber registered
	time.Sleep(50 * time.Millisecond)
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 1 {
		t.Fatalf("subscriber count = %d, want 1", count)
	}

	// Cancel context to simulate client disconnect
	cancel()
	res.Body.Close()

	// Wait for handler cleanup
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 0 {
		t.Errorf("subscriber count after disconnect = %d, want 0 (leak!)", count)
	}
}

func TestEvents_SSE_BurstWritesDoNotBlock(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)

	proj := createProject(t, stk, "Burst Project")

	// Connect a slow client that never reads body
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer res.Body.Close()

	// Mutating burst: 100 task creations in rapid succession
	start := time.Now()
	for i := 0; i < 100; i++ {
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
			"project_id": proj.ID,
			"title":      fmt.Sprintf("Burst Task %d", i),
			"actor":      actor("ada"),
		})
		if code != http.StatusCreated {
			t.Fatalf("task creation failed at %d: %d (%s)", i, code, body)
		}
	}
	elapsed := time.Since(start)

	// 100 SQLite commits should complete in under 5 seconds, not blocked by slow SSE client
	if elapsed > 5*time.Second {
		t.Errorf("burst of 100 writes took %v, expected non-blocking execution under 5s", elapsed)
	}
}
