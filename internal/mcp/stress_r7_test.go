package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/ravn/internal/interface/http"
)

// ======================================================================================
// R7 EMPIRICAL TESTS: MCP clear_due & clear_assignee Tool Schema & Round-Trip
// ======================================================================================

// TestChallenger_R7_ToolSchemaDefinition directly inspects the JSON schema of update_task
// as registered with the MCP server to verify that clear_due and clear_assignee are exposed
// as booleans with appropriate descriptions.
func TestChallenger_R7_ToolSchemaDefinition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, _ := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("c.ListTools failed: %v", err)
	}

	var updateTool *mcpgo.Tool
	for i := range list.Tools {
		if list.Tools[i].Name == "update_task" {
			updateTool = &list.Tools[i]
			break
		}
	}
	if updateTool == nil {
		t.Fatalf("update_task tool not found in registered MCP tools")
	}

	props := updateTool.InputSchema.Properties
	if props == nil {
		t.Fatalf("update_task InputSchema.Properties is nil")
	}

	// 1. Check clear_due property
	clearDueRaw, ok := props["clear_due"]
	if !ok {
		t.Fatalf("update_task schema missing 'clear_due' property")
	}
	clearDueProp, ok := clearDueRaw.(map[string]any)
	if !ok {
		t.Fatalf("update_task 'clear_due' property is not a map[string]any: %T", clearDueRaw)
	}
	if clearDueProp["type"] != "boolean" {
		t.Errorf("update_task 'clear_due' type = %v, want 'boolean'", clearDueProp["type"])
	}

	// 2. Check clear_assignee property
	clearAssigneeRaw, ok := props["clear_assignee"]
	if !ok {
		t.Fatalf("update_task schema missing 'clear_assignee' property")
	}
	clearAssigneeProp, ok := clearAssigneeRaw.(map[string]any)
	if !ok {
		t.Fatalf("update_task 'clear_assignee' property is not a map[string]any: %T", clearAssigneeRaw)
	}
	if clearAssigneeProp["type"] != "boolean" {
		t.Errorf("update_task 'clear_assignee' type = %v, want 'boolean'", clearAssigneeProp["type"])
	}

	// Verify neither clear_due nor clear_assignee is in Required
	for _, req := range updateTool.InputSchema.Required {
		if req == "clear_due" || req == "clear_assignee" {
			t.Errorf("flag %q should be optional, but is listed in Required", req)
		}
	}
}

// TestChallenger_R7_SchemaValidation_TypeRejections sends non-boolean values to
// clear_due and clear_assignee to verify that the MCP server's input schema validator
// rejects them with isError: true.
func TestChallenger_R7_SchemaValidation_TypeRejections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, _ := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	invalidValues := []struct {
		desc  string
		value any
	}{
		{"string 'true'", "true"},
		{"string 'false'", "false"},
		{"empty string", ""},
		{"integer 1", 1},
		{"integer 0", 0},
		{"float 1.0", 1.0},
		{"slice", []any{true}},
		{"map", map[string]any{"clear": true}},
	}

	// Test clear_due type rejections
	for _, tc := range invalidValues {
		t.Run("clear_due/"+tc.desc, func(t *testing.T) {
			res := callTool(t, c, ctx, "update_task", map[string]any{
				"id":        "T1",
				"version":   4,
				"clear_due": tc.value,
			})
			if !res.IsError {
				t.Fatalf("expected isError=true for clear_due=%v (%s), got success: %s",
					tc.value, tc.desc, firstText(t, res))
			}
		})
	}

	// Test clear_assignee type rejections
	for _, tc := range invalidValues {
		t.Run("clear_assignee/"+tc.desc, func(t *testing.T) {
			res := callTool(t, c, ctx, "update_task", map[string]any{
				"id":             "T1",
				"version":        4,
				"clear_assignee": tc.value,
			})
			if !res.IsError {
				t.Fatalf("expected isError=true for clear_assignee=%v (%s), got success: %s",
					tc.value, tc.desc, firstText(t, res))
			}
		})
	}
}

// TestChallenger_R7_PayloadMapping_AllPermutations tests all 9 permutations of
// (clear_due: [omitted, false, true]) x (clear_assignee: [omitted, false, true])
// to verify exact wire decoding and mapping to apiclient.UpdateTaskRequest.
func TestChallenger_R7_PayloadMapping_AllPermutations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	type testCase struct {
		name              string
		args              map[string]any
		wantClearDue      bool
		wantClearAssignee bool
	}

	tests := []testCase{
		{
			name:         "omitted_omitted",
			args:         map[string]any{"id": "T1", "version": 4},
			wantClearDue: false, wantClearAssignee: false,
		},
		{
			name:         "omitted_false",
			args:         map[string]any{"id": "T1", "version": 4, "clear_assignee": false},
			wantClearDue: false, wantClearAssignee: false,
		},
		{
			name:         "omitted_true",
			args:         map[string]any{"id": "T1", "version": 4, "clear_assignee": true},
			wantClearDue: false, wantClearAssignee: true,
		},
		{
			name:         "false_omitted",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": false},
			wantClearDue: false, wantClearAssignee: false,
		},
		{
			name:         "false_false",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": false, "clear_assignee": false},
			wantClearDue: false, wantClearAssignee: false,
		},
		{
			name:         "false_true",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": false, "clear_assignee": true},
			wantClearDue: false, wantClearAssignee: true,
		},
		{
			name:         "true_omitted",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": true},
			wantClearDue: true, wantClearAssignee: false,
		},
		{
			name:         "true_false",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": true, "clear_assignee": false},
			wantClearDue: true, wantClearAssignee: false,
		},
		{
			name:         "true_true",
			args:         map[string]any{"id": "T1", "version": 4, "clear_due": true, "clear_assignee": true},
			wantClearDue: true, wantClearAssignee: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCountBefore := len(*calls)
			res := callTool(t, c, ctx, "update_task", tt.args)
			if res.IsError {
				t.Fatalf("update_task returned unexpected error: %s", firstText(t, res))
			}

			if len(*calls) != callCountBefore+1 {
				t.Fatalf("expected 1 new call, got %d", len(*calls)-callCountBefore)
			}
			lastCall := (*calls)[len(*calls)-1]
			if lastCall.method != http.MethodPatch || lastCall.path != "/api/v1/tasks/T1" {
				t.Fatalf("unexpected call: %s %s", lastCall.method, lastCall.path)
			}

			var req apiclient.UpdateTaskRequest
			if err := json.Unmarshal([]byte(lastCall.body), &req); err != nil {
				t.Fatalf("failed to unmarshal request body: %v", err)
			}

			if req.ClearDue != tt.wantClearDue {
				t.Errorf("ClearDue = %v, want %v", req.ClearDue, tt.wantClearDue)
			}
			if req.ClearAssignee != tt.wantClearAssignee {
				t.Errorf("ClearAssignee = %v, want %v", req.ClearAssignee, tt.wantClearAssignee)
			}
		})
	}
}

// TestChallenger_R7_ConflictingInputs_DeterministicDomain verifies that when an agent
// sends conflicting inputs (e.g. clear_due: true with due_at: "...", or clear_assignee: true
// with assignee: {...}):
// 1. The MCP tool faithfully forwards both values to the API client payload.
// 2. The domain / application logic deterministically executes the clearing semantics.
func TestChallenger_R7_ConflictingInputs_DeterministicDomain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// 1. Conflicting clear_due: true AND due_at: "2026-10-15T00:00:00Z"
	t.Run("conflict_clear_due_with_due_at", func(t *testing.T) {
		callCountBefore := len(*calls)
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":        "T1",
			"version":   4,
			"clear_due": true,
			"due_at":    "2026-10-15T00:00:00Z",
		})
		if res.IsError {
			t.Fatalf("update_task returned error: %s", firstText(t, res))
		}
		lastCall := (*calls)[len(*calls)-1]
		if len(*calls) != callCountBefore+1 {
			t.Fatalf("call was not captured")
		}

		var req apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(lastCall.body), &req); err != nil {
			t.Fatalf("body unmarshal: %v", err)
		}
		if !req.ClearDue {
			t.Errorf("expected ClearDue == true in request payload")
		}
		if req.DueAt == nil || *req.DueAt != "2026-10-15T00:00:00Z" {
			t.Errorf("expected DueAt to be forwarded along with ClearDue")
		}
	})

	// 2. Conflicting clear_assignee: true AND assignee: {name: "ada", type: "human"}
	t.Run("conflict_clear_assignee_with_assignee", func(t *testing.T) {
		callCountBefore := len(*calls)
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":             "T1",
			"version":        4,
			"clear_assignee": true,
			"assignee":       map[string]any{"name": "ada", "type": "human"},
		})
		if res.IsError {
			t.Fatalf("update_task returned error: %s", firstText(t, res))
		}
		lastCall := (*calls)[len(*calls)-1]
		if len(*calls) != callCountBefore+1 {
			t.Fatalf("call was not captured")
		}

		var req apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(lastCall.body), &req); err != nil {
			t.Fatalf("body unmarshal: %v", err)
		}
		if !req.ClearAssignee {
			t.Errorf("expected ClearAssignee == true in request payload")
		}
		if req.Assignee == nil || req.Assignee.Name != "ada" {
			t.Errorf("expected Assignee to be forwarded along with ClearAssignee")
		}
	})
}

// TestChallenger_R7_FullStack_LiveSQLiteRoundTrip builds the entire real stack:
// SQLite on disk (temp file) -> UnitOfWork -> Tasks use case -> HTTP App ->
// Live HTTP server -> apiclient -> MCP Server -> Streamable HTTP -> mcpclient.Client.
// It verifies that clear_due and clear_assignee round-trip end-to-end and directly
// checks the SQLite database columns to prove real database persistence.
func TestChallenger_R7_FullStack_LiveSQLiteRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Stand up real SQLite database
	dbPath := filepath.Join(t.TempDir(), "challenger_r7_live.db")
	db, closeDB, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = closeDB() })

	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
	now := clock.New()
	mint := idgen.New()

	app := httpapi.NewApp(httpapi.Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
		Broker:     broker,
	})

	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, app)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	// 2. Stand up real MCP server pointing to the live HTTP server
	api := apiclient.New(httpServer.URL, "")
	mcs := mcpsrv.NewMCPServer("ravn", "1.0.0", ServerOptions()...)
	RegisterTools(mcs, api, apiclient.Actor{Type: "agent", Name: "challenger-agent"})
	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
	)
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", streamable)
	mcpServer := httptest.NewServer(mcpMux)
	t.Cleanup(mcpServer.Close)

	// 3. Connect real MCP client
	c := dial(t, mcpServer.URL, ctx)
	defer func() { _ = c.Close() }()

	// Step A: Create a project
	projRes := callTool(t, c, ctx, "create_project", map[string]any{
		"name":        "Empirical R7 Project",
		"description": "Project for live verification",
	})
	if projRes.IsError {
		t.Fatalf("create_project failed: %s", firstText(t, projRes))
	}
	projData := asMap(t, firstText(t, projRes))
	projectID := projData["id"].(string)

	// Step B: Create a task with due_at and assignee
	taskRes := callTool(t, c, ctx, "create_task", map[string]any{
		"project_id": projectID,
		"title":      "Task With Due and Assignee",
		"due_at":     "2026-11-15T10:00:00Z",
		"assignee":   map[string]any{"name": "alice", "type": "human"},
	})
	if taskRes.IsError {
		t.Fatalf("create_task failed: %s", firstText(t, taskRes))
	}
	taskCreated := asMap(t, firstText(t, taskRes))
	taskSummary := taskCreated["task"].(map[string]any)
	taskID := taskSummary["id"].(string)
	version := int(taskSummary["version"].(float64))

	// Verify task in SQLite directly
	assertTaskInDB(t, db, taskID, true, true)

	// Step C: update_task with clear_due: true
	updDueRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":        taskID,
		"version":   version,
		"clear_due": true,
	})
	if updDueRes.IsError {
		t.Fatalf("update_task clear_due failed: %s", firstText(t, updDueRes))
	}
	updDueData := asMap(t, firstText(t, updDueRes))
	if updDueData["due_at"] != nil {
		t.Errorf("expected due_at=nil in response, got %v", updDueData["due_at"])
	}
	if updDueData["assignee"] == nil {
		t.Errorf("expected assignee to be preserved, got nil")
	}
	version = int(updDueData["version"].(float64))

	// Verify in SQLite: due_at is NULL, assignee is preserved
	assertTaskInDB(t, db, taskID, false, true)

	// Step D: update_task with clear_assignee: true
	updAssigneeRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":             taskID,
		"version":        version,
		"clear_assignee": true,
	})
	if updAssigneeRes.IsError {
		t.Fatalf("update_task clear_assignee failed: %s", firstText(t, updAssigneeRes))
	}
	updAssigneeData := asMap(t, firstText(t, updAssigneeRes))
	if updAssigneeData["assignee"] != nil {
		t.Errorf("expected assignee=nil in response, got %v", updAssigneeData["assignee"])
	}
	version = int(updAssigneeData["version"].(float64))

	// Verify in SQLite: due_at is NULL, assignee is NULL
	assertTaskInDB(t, db, taskID, false, false)

	// Step E: Re-populate due_at and assignee
	repRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":       taskID,
		"version":  version,
		"due_at":   "2026-12-01T00:00:00Z",
		"assignee": map[string]any{"name": "bob", "type": "agent"},
	})
	if repRes.IsError {
		t.Fatalf("repopulate update_task failed: %s", firstText(t, repRes))
	}
	repData := asMap(t, firstText(t, repRes))
	if repData["due_at"] == nil {
		t.Errorf("expected due_at set, got nil")
	}
	if repData["assignee"] == nil {
		t.Errorf("expected assignee set, got nil")
	}
	version = int(repData["version"].(float64))
	assertTaskInDB(t, db, taskID, true, true)

	// Step F: Atomic clear of BOTH clear_due: true AND clear_assignee: true in ONE update
	bothRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":             taskID,
		"version":        version,
		"clear_due":      true,
		"clear_assignee": true,
	})
	if bothRes.IsError {
		t.Fatalf("both clear failed: %s", firstText(t, bothRes))
	}
	bothData := asMap(t, firstText(t, bothRes))
	if bothData["due_at"] != nil {
		t.Errorf("expected due_at=nil, got %v", bothData["due_at"])
	}
	if bothData["assignee"] != nil {
		t.Errorf("expected assignee=nil, got %v", bothData["assignee"])
	}
	version = int(bothData["version"].(float64))
	assertTaskInDB(t, db, taskID, false, false)

	// Step G: Stale version test returns 409 version conflict isError
	staleRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":        taskID,
		"version":   1, // old version
		"clear_due": true,
	})
	if !staleRes.IsError {
		t.Fatalf("expected isError for stale version 1, got success: %s", firstText(t, staleRes))
	}
	staleText := firstText(t, staleRes)
	if !strings.Contains(staleText, "version_conflict") {
		t.Errorf("expected 'version_conflict' in error response, got %s", staleText)
	}

	// Step H: Domain priority resolution when both clear_due: true AND due_at: "..." are sent
	// Re-populate due_at first
	repopDue := callTool(t, c, ctx, "update_task", map[string]any{
		"id":      taskID,
		"version": version,
		"due_at":  "2027-01-01T00:00:00Z",
	})
	if repopDue.IsError {
		t.Fatalf("repopulate due failed: %s", firstText(t, repopDue))
	}
	version = int(asMap(t, firstText(t, repopDue))["version"].(float64))
	assertTaskInDB(t, db, taskID, true, false)

	// Now send BOTH clear_due: true AND due_at: "2028-01-01T00:00:00Z"
	// Domain rule: clear_due takes precedence, deadline is cleared!
	conflictDueRes := callTool(t, c, ctx, "update_task", map[string]any{
		"id":        taskID,
		"version":   version,
		"clear_due": true,
		"due_at":    "2028-01-01T00:00:00Z",
	})
	if conflictDueRes.IsError {
		t.Fatalf("conflict due update returned error: %s", firstText(t, conflictDueRes))
	}
	conflictData := asMap(t, firstText(t, conflictDueRes))
	if conflictData["due_at"] != nil {
		t.Errorf("expected clear_due to take precedence over due_at, but due_at = %v", conflictData["due_at"])
	}
	assertTaskInDB(t, db, taskID, false, false)
}

// assertTaskInDB executes raw SQL against SQLite to verify exact column values in storage.
func assertTaskInDB(t *testing.T, db *sql.DB, taskID string, wantDue bool, wantAssignee bool) {
	t.Helper()
	row := db.QueryRow(`SELECT due_at, assignee_type, assignee_name FROM task WHERE id = ?`, taskID)
	var dueAt, assigneeType, assigneeName sql.NullString
	if err := row.Scan(&dueAt, &assigneeType, &assigneeName); err != nil {
		t.Fatalf("Scan task %s failed: %v", taskID, err)
	}

	if wantDue && !dueAt.Valid {
		t.Errorf("expected due_at in SQLite to be NON-NULL, got NULL")
	} else if !wantDue && dueAt.Valid {
		t.Errorf("expected due_at in SQLite to be NULL, got %q", dueAt.String)
	}

	if wantAssignee && (!assigneeType.Valid || !assigneeName.Valid) {
		t.Errorf("expected assignee in SQLite to be NON-NULL, got type=%v name=%v", assigneeType, assigneeName)
	} else if !wantAssignee && (assigneeType.Valid || assigneeName.Valid) {
		t.Errorf("expected assignee in SQLite to be NULL, got type=%q name=%q", assigneeType.String, assigneeName.String)
	}
}

func TestUpdateTaskDescription_LiveSQLiteRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Stand up real SQLite database
	dbPath := filepath.Join(t.TempDir(), "live_desc_test.db")
	db, closeDB, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = closeDB() })

	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
	now := clock.New()
	mint := idgen.New()

	app := httpapi.NewApp(httpapi.Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
		Broker:     broker,
	})

	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, app)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	// 2. Stand up real MCP server pointing to the live HTTP server
	api := apiclient.New(httpServer.URL, "")
	mcs := mcpsrv.NewMCPServer("ravn", "1.0.0", ServerOptions()...)
	RegisterTools(mcs, api, apiclient.Actor{Type: "agent", Name: "test-agent"})
	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
	)
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", streamable)
	mcpServer := httptest.NewServer(mcpMux)
	t.Cleanup(mcpServer.Close)

	// 3. Connect real MCP client
	c := dial(t, mcpServer.URL, ctx)
	defer func() { _ = c.Close() }()

	// Step A: Create a project
	projRes := callTool(t, c, ctx, "create_project", map[string]any{
		"name": "Description Test Project",
	})
	if projRes.IsError {
		t.Fatalf("create_project failed: %s", firstText(t, projRes))
	}
	projectID := asMap(t, firstText(t, projRes))["id"].(string)

	// Step B: Create a task with initial description
	taskRes := callTool(t, c, ctx, "create_task", map[string]any{
		"project_id":  projectID,
		"title":       "Task with description",
		"description": "Initial description text",
	})
	if taskRes.IsError {
		t.Fatalf("create_task failed: %s", firstText(t, taskRes))
	}
	taskCreated := asMap(t, firstText(t, taskRes))
	taskSummary := taskCreated["task"].(map[string]any)
	taskID := taskSummary["id"].(string)
	version := int(taskSummary["version"].(float64))

	// Verify initial description in SQLite
	assertTaskDescriptionInDB(t, db, taskID, "Initial description text")

	// Step C: update_task omitting description leaves existing description unchanged (AC 2)
	updNoDesc := callTool(t, c, ctx, "update_task", map[string]any{
		"id":      taskID,
		"version": version,
		"title":   "Updated Title Only",
	})
	if updNoDesc.IsError {
		t.Fatalf("update_task without description failed: %s", firstText(t, updNoDesc))
	}
	updNoDescData := asMap(t, firstText(t, updNoDesc))
	version = int(updNoDescData["version"].(float64))
	assertTaskDescriptionInDB(t, db, taskID, "Initial description text")

	// Step D: update_task with description: "" clears task description in database (AC 1)
	updClearDesc := callTool(t, c, ctx, "update_task", map[string]any{
		"id":          taskID,
		"version":     version,
		"description": "",
	})
	if updClearDesc.IsError {
		t.Fatalf("update_task with description: \"\" failed: %s", firstText(t, updClearDesc))
	}
	updClearData := asMap(t, firstText(t, updClearDesc))
	version = int(updClearData["version"].(float64))
	assertTaskDescriptionInDB(t, db, taskID, "")

	// Step E: update_task with new description sets new text
	updNewDesc := callTool(t, c, ctx, "update_task", map[string]any{
		"id":          taskID,
		"version":     version,
		"description": "Brand new description",
	})
	if updNewDesc.IsError {
		t.Fatalf("update_task with new description failed: %s", firstText(t, updNewDesc))
	}
	updNewData := asMap(t, firstText(t, updNewDesc))
	version = int(updNewData["version"].(float64))
	assertTaskDescriptionInDB(t, db, taskID, "Brand new description")

	// Step F: update_task with whitespace description "   " also clears description
	updWhitespaceDesc := callTool(t, c, ctx, "update_task", map[string]any{
		"id":          taskID,
		"version":     version,
		"description": "   ",
	})
	if updWhitespaceDesc.IsError {
		t.Fatalf("update_task with whitespace description failed: %s", firstText(t, updWhitespaceDesc))
	}
	assertTaskDescriptionInDB(t, db, taskID, "")
}

func assertTaskDescriptionInDB(t *testing.T, db *sql.DB, taskID string, want string) {
	t.Helper()
	row := db.QueryRow(`SELECT description FROM task WHERE id = ?`, taskID)
	var got string
	if err := row.Scan(&got); err != nil {
		t.Fatalf("Scan task description for %s failed: %v", taskID, err)
	}
	if got != want {
		t.Errorf("task %s description in SQLite = %q, want %q", taskID, got, want)
	}
}
