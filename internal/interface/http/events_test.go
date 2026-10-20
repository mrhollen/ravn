package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
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
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ravn-realtime-test.db"))
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
		Events:     sqlite.NewEventStore(db), // Last-Event-ID replay path (pool-bound)
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

type testDeadlineRecorder struct {
	http.ResponseWriter
	mu        sync.Mutex
	deadlines []time.Time
	flushErr  error
	writeErr  error
}

func (r *testDeadlineRecorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, t)
	return nil
}

func (r *testDeadlineRecorder) Flush() {}

func (r *testDeadlineRecorder) FlushError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushErr
}

func (r *testDeadlineRecorder) SetFlushErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushErr = err
}

func (r *testDeadlineRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	err := r.writeErr
	r.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return r.ResponseWriter.Write(p)
}

func (r *testDeadlineRecorder) Deadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]time.Time, len(r.deadlines))
	copy(cp, r.deadlines)
	return cp
}

func TestEvents_SSE_ConfiguresAndClearsWriteDeadlines(t *testing.T) {
	stk, _, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Deadline Project")

	rec := &testDeadlineRecorder{
		ResponseWriter: httptest.NewRecorder(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/projects/"+proj.ID+"/events", nil)

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		stk.Handler.ServeHTTP(rec, req)
	}()

	// Wait for initial : connected write to execute and deadlines recorded
	deadline := time.Now().Add(1 * time.Second)
	var dls []time.Time
	for time.Now().Before(deadline) {
		dls = rec.Deadlines()
		if len(dls) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(dls) < 2 {
		t.Fatalf("expected at least 2 deadline calls (set + clear), got %d", len(dls))
	}

	firstDeadline := dls[0]
	if firstDeadline.IsZero() || !firstDeadline.After(time.Now()) {
		t.Errorf("expected first deadline to be in future, got %v", firstDeadline)
	}

	clearedDeadline := dls[1]
	if !clearedDeadline.IsZero() {
		t.Errorf("expected second deadline to be zero (cleared), got %v", clearedDeadline)
	}

	cancel()
	<-doneCh
}

func TestEvents_SSE_WriteErrorCleansUpBrokerSubscription(t *testing.T) {
	stk, broker, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Write Error Project")

	rec := &testDeadlineRecorder{
		ResponseWriter: httptest.NewRecorder(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/projects/"+proj.ID+"/events", nil)

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		stk.Handler.ServeHTTP(rec, req)
	}()

	// Wait for subscription
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 1 {
		t.Fatalf("subscriber count = %d, want 1", count)
	}

	// Arm recorder to return an error on next flush (simulating write failure / deadline exceeded)
	rec.SetFlushErr(errors.New("i/o timeout: write deadline exceeded"))

	// Trigger an event to force a write
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"project_id": proj.ID,
		"title":      "Trigger Task",
		"actor":      actor("ada"),
	})
	if code != http.StatusCreated {
		t.Fatalf("create task failed: %d (%s)", code, body)
	}

	// Handler should detect the flushErr and exit immediately
	select {
	case <-doneCh:
		// Success: handler terminated cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not terminate after write error")
	}

	// Verify subscriber was removed from broker
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 0 {
		t.Errorf("subscriber count = %d, want 0 after write error (leak!)", count)
	}
}

func TestEvents_SSE_ClientAbruptDisconnectOnEventCleansUp(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Abrupt Disconnect Project")

	// Connect SSE client
	res, err := srv.Client().Get(srv.URL + "/api/v1/projects/" + proj.ID + "/events")
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}

	// Wait for subscriber registration
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 1 {
		t.Fatalf("subscriber count = %d, want 1", count)
	}

	// Abruptly close response body without cancelling request context
	_ = res.Body.Close()

	// Trigger a task creation event so server attempts to write to broken connection
	newTask(t, stk, proj.ID, "Post Disconnect Task")

	// Wait for subscriber cleanup
	deadline = time.Now().Add(2 * time.Second)
	cleaned := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 0 {
			cleaned = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !cleaned {
		t.Errorf("subscriber count was not cleaned up after client disconnect and event write (leak!)")
	}
}

// --- Last-Event-ID replay --------------------------------------------------------

// sseConnect opens an SSE stream on the given absolute URL. lastEventIDHeader
// sets the WHATWG auto-reconnect header; query cursors are encoded by the
// caller in the URL itself. The helper asserts 200 + text/event-stream and
// closes the body on cleanup.
func sseConnect(t *testing.T, srv *httptest.Server, url, lastEventIDHeader string) (*bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		cancel()
		t.Fatalf("new SSE request: %v", err)
	}
	if lastEventIDHeader != "" {
		req.Header.Set("Last-Event-ID", lastEventIDHeader)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect SSE: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect status = %d, want 200 (cursor %q)", res.StatusCode, lastEventIDHeader)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream", ct)
	}
	return bufio.NewReader(res.Body), cancel
}

// readSSETaskEvents reads n SSE frames and returns them; each frame's task is
// decoded from the realtime data payload so callers can assert on identity.
func readSSETaskEvents(t *testing.T, r *bufio.Reader, n int) []sseEvent {
	t.Helper()
	out := make([]sseEvent, 0, n)
	for i := 0; i < n; i++ {
		ev, err := readSSEEvent(r, 3*time.Second)
		if err != nil {
			t.Fatalf("read SSE event %d/%d: %v", i+1, n, err)
		}
		out = append(out, ev)
	}
	return out
}

func taskIDOfSSEFrame(t *testing.T, ev sseEvent) string {
	t.Helper()
	var data realtimeEventDTO
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("unmarshal SSE frame %q: %v", ev.Data, err)
	}
	return data.TaskID
}

// TestEvents_SSE_LastEventIDReplaysMissedEventsHeader is the core AC: a client
// that watched A and B live, dropped, and reconnects with Last-Event-ID = B's
// frame id receives exactly C (created during the drop) BEFORE any live events
// — no duplicates of A/B, no gap.
func TestEvents_SSE_LastEventIDReplaysMissedEventsHeader(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Replay Header Project")
	url := srv.URL + "/api/v1/projects/" + proj.ID + "/events"

	// Client 1 watches A and B live.
	reader1, cancel1 := sseConnect(t, srv, url, "")
	taskA := newTask(t, stk, proj.ID, "replay task A")
	evA := readSSETaskEvents(t, reader1, 1)[0]
	if taskIDOfSSEFrame(t, evA) != taskA.ID {
		t.Fatalf("frame for A has task %q, want %q", taskIDOfSSEFrame(t, evA), taskA.ID)
	}
	newTask(t, stk, proj.ID, "replay task B")
	evB := readSSETaskEvents(t, reader1, 1)[0]
	if evB.ID == "" {
		t.Fatal("frame id is empty — replay cursor cannot work without ids on frames")
	}

	// Client 1 drops (simulating a 1-3s network interruption).
	cancel1()

	// C happens while nobody is listening — it only exists in the event log.
	taskC := newTask(t, stk, proj.ID, "replay task C")

	// Reconnect with the LAST received frame id (B). Expect replay of C first,
	// then live D — in that exact order, nothing else.
	reader2, cancel2 := sseConnect(t, srv, url, evB.ID)
	defer cancel2()
	taskD := newTask(t, stk, proj.ID, "replay task D")

	frames := readSSETaskEvents(t, reader2, 2)
	gotTasks := []string{taskIDOfSSEFrame(t, frames[0]), taskIDOfSSEFrame(t, frames[1])}
	want := []string{taskC.ID, taskD.ID}
	for i := range want {
		if gotTasks[i] != want[i] {
			t.Fatalf("reconnect frames = %v (kinds %v), want %v — missed events must be replayed before live",
				gotTasks, []string{frames[0].Event, frames[1].Event}, want)
		}
	}
	// The replayed frame carries the same id C had in the log (no re-mint).
	if frames[0].ID == "" || frames[0].Event != "created" {
		t.Fatalf("replayed frame = id %q kind %q, want a created frame with an id", frames[0].ID, frames[0].Event)
	}
}

// createdEventID returns the id of a fresh task's "created" event — the only
// event a just-created task has — read straight from the resume payload.
func createdEventID(t *testing.T, stk stack, taskID string) string {
	t.Helper()
	d := getDetail(t, stk, taskID)
	if len(d.Events) == 0 || d.Events[0].Kind != "created" {
		t.Fatalf("task %s detail events = %+v, want the created event first", taskID, d.Events)
	}
	return d.Events[0].ID
}

// TestEvents_SSE_LastEventIDQueryParamReplays covers the second cursor source
// (last_event_id query parameter on the ?project_id= route) with a LOWERCASE
// cursor — non-browser clients may mangle case; stored ids are uppercase, so
// the handler must normalize before comparing.
func TestEvents_SSE_LastEventIDQueryParamReplays(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Replay Query Project")

	newTask(t, stk, proj.ID, "q-replay task A") // seeded with no listener; cursor must skip past it
	taskB := newTask(t, stk, proj.ID, "q-replay task B")
	idB := createdEventID(t, stk, taskB.ID)
	taskC := newTask(t, stk, proj.ID, "q-replay task C")

	// Reconnect via the query-parameter route with a lowercase cursor.
	url := fmt.Sprintf("%s/api/v1/events?project_id=%s&last_event_id=%s", srv.URL, proj.ID, strings.ToLower(idB))
	reader, cancel := sseConnect(t, srv, url, "")
	defer cancel()

	frames := readSSETaskEvents(t, reader, 1)
	if got := taskIDOfSSEFrame(t, frames[0]); got != taskC.ID {
		t.Fatalf("query-param replay frame = task %q kind %q, want task %q — lowercase cursor must still replay C (and only C)", got, frames[0].Event, taskC.ID)
	}
	if frames[0].ID != createdEventID(t, stk, taskC.ID) {
		t.Errorf("replayed frame id = %q, want the id C has in the event log", frames[0].ID)
	}
}

// TestEvents_SSE_LastEventIDHeaderTakesPrecedence proves the documented rule:
// when BOTH cursors are present and disagree, the Last-Event-ID header wins.
func TestEvents_SSE_LastEventIDHeaderTakesPrecedence(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Replay Precedence Project")

	taskA := newTask(t, stk, proj.ID, "prec task A")
	taskB := newTask(t, stk, proj.ID, "prec task B")
	taskC := newTask(t, stk, proj.ID, "prec task C")

	idA := createdEventID(t, stk, taskA.ID) // header cursor: replays [B, C]
	idC := createdEventID(t, stk, taskC.ID) // query cursor:  replays []

	url := fmt.Sprintf("%s/api/v1/projects/%s/events?last_event_id=%s", srv.URL, proj.ID, idC)
	reader, cancel := sseConnect(t, srv, url, idA)
	defer cancel()
	taskD := newTask(t, stk, proj.ID, "prec live D") // published after this subscription

	// Header wins ⇒ replay B then C, THEN live D. (Query winning ⇒ first frame
	// would be D and the read below fails on frame 0/1.)
	frames := readSSETaskEvents(t, reader, 3)
	got := []string{taskIDOfSSEFrame(t, frames[0]), taskIDOfSSEFrame(t, frames[1]), taskIDOfSSEFrame(t, frames[2])}
	want := []string{taskB.ID, taskC.ID, taskD.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d = task %q, want %q (Last-Event-ID header must take precedence over the query param)", i, got[i], want[i])
		}
	}
}

// TestEvents_SSE_InvalidLastEventIDIsIgnoredLivesOnly: a malformed cursor must
// never error the connection (200 + stream) and must NOT trigger a replay —
// the first frame is the live event, not the pre-seeded one.
func TestEvents_SSE_InvalidLastEventIDIsIgnoredLivesOnly(t *testing.T) {
	stk, _, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Replay Invalid Project")

	newTask(t, stk, proj.ID, "inv-seeded task") // would appear FIRST if replay were (wrongly) attempted
	url := srv.URL + "/api/v1/projects/" + proj.ID + "/events"

	badCursors := []string{
		"not-a-ulid",            // wrong chars and length
		strings.Repeat("O", 26), // O is excluded from Crockford base32
		strings.Repeat("0", 25), // one char short of a ULID
	}
	for _, bad := range badCursors {
		reader, cancel := sseConnect(t, srv, url, bad)              // must still return 200
		fresh := newTask(t, stk, proj.ID, "inv live after connect") // published AFTER this subscription
		frames := readSSETaskEvents(t, reader, 1)
		if got := taskIDOfSSEFrame(t, frames[0]); got != fresh.ID {
			t.Fatalf("cursor %q: first frame = task %q, want the LIVE task %q (invalid cursor must be ignored, not replayed)", bad, got, fresh.ID)
		}
		cancel()
	}
}
