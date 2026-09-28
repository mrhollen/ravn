package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// mockClient implements Client for prompt unit testing.
type mockClient struct {
	baseURLFunc      func() string
	listProjectsFunc func(ctx context.Context) (*apiclient.ProjectList, error)
	listTasksFunc    func(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error)
	getTaskFunc      func(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error)
}

func (m *mockClient) BaseURL() string {
	if m.baseURLFunc != nil {
		return m.baseURLFunc()
	}
	return "http://localhost:8080"
}

func (m *mockClient) ListProjects(ctx context.Context) (*apiclient.ProjectList, error) {
	if m.listProjectsFunc != nil {
		return m.listProjectsFunc(ctx)
	}
	return &apiclient.ProjectList{}, nil
}

func (m *mockClient) CreateProject(ctx context.Context, req apiclient.CreateProjectRequest) (*apiclient.Project, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) ListTasks(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error) {
	if m.listTasksFunc != nil {
		return m.listTasksFunc(ctx, f)
	}
	return &apiclient.TaskList{}, nil
}

func (m *mockClient) GetTask(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error) {
	if m.getTaskFunc != nil {
		return m.getTaskFunc(ctx, id, commentLimit, eventLimit)
	}
	return &apiclient.TaskDetail{
		TaskSummary: apiclient.TaskSummary{ID: id, Title: "Default Task", Status: "todo", Priority: 2, Version: 1},
	}, nil
}

func (m *mockClient) CreateTask(ctx context.Context, req apiclient.CreateTaskRequest) (*apiclient.CreateTaskResponse, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) UpdateTask(ctx context.Context, id string, req apiclient.UpdateTaskRequest) (*apiclient.TaskSummary, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) ClaimTask(ctx context.Context, id string, actor apiclient.Actor, ttlSec int) (*apiclient.TaskSummary, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) ReleaseTask(ctx context.Context, id string, actor apiclient.Actor, note string) (*apiclient.TaskSummary, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) HeartbeatTask(ctx context.Context, id string, actor apiclient.Actor) (*apiclient.TaskSummary, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) AddComment(ctx context.Context, id string, req apiclient.CommentRequest) (*apiclient.Comment, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) UpdateNotes(ctx context.Context, id string, actor apiclient.Actor, notes string) (*apiclient.TaskSummary, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) CompleteAC(ctx context.Context, id string, seq int, req apiclient.ACToggleRequest) (*apiclient.AcceptanceCriterion, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) AttachArtifact(ctx context.Context, id string, req apiclient.ArtifactRequest) (*apiclient.Artifact, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) RelateTasks(ctx context.Context, id string, req apiclient.RelationRequest) (*apiclient.Relation, error) {
	return nil, errors.New("unimplemented")
}

func (m *mockClient) UnrelateTasks(ctx context.Context, id string, actor apiclient.Actor, otherID, kind string) error {
	return errors.New("unimplemented")
}

// TestPromptsList_Discoverability validates Acceptance Criterion 1:
// "At least one prompt is discoverable via MCP prompts/list with a description and args schema."
func TestPromptsList_Discoverability(t *testing.T) {
	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	mock := &mockClient{}
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}
	RegisterPrompts(mcs, mock, defaultActor)

	listMsg := `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`
	resp := mcs.HandleMessage(context.Background(), json.RawMessage(listMsg))

	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	res, ok := jsonResp.Result.(mcpgo.ListPromptsResult)
	if !ok {
		t.Fatalf("expected ListPromptsResult, got %T: %+v", jsonResp.Result, jsonResp.Result)
	}

	if len(res.Prompts) < 2 {
		t.Fatalf("expected at least 2 prompts, got %d", len(res.Prompts))
	}

	promptMap := make(map[string]mcpgo.Prompt)
	for _, p := range res.Prompts {
		promptMap[p.Name] = p
	}

	// 1. Validate triage_open_tasks
	triage, ok := promptMap["triage_open_tasks"]
	if !ok {
		t.Fatal("prompt triage_open_tasks not found in prompts/list")
	}
	if strings.TrimSpace(triage.Description) == "" {
		t.Error("triage_open_tasks description must not be empty")
	}
	triageArgs := make(map[string]mcpgo.PromptArgument)
	for _, a := range triage.Arguments {
		triageArgs[a.Name] = a
	}
	if _, ok := triageArgs["project_id"]; !ok {
		t.Error("triage_open_tasks missing project_id argument schema")
	}
	if _, ok := triageArgs["limit"]; !ok {
		t.Error("triage_open_tasks missing limit argument schema")
	}

	// 2. Validate next_task
	next, ok := promptMap["next_task"]
	if !ok {
		t.Fatal("prompt next_task not found in prompts/list")
	}
	if strings.TrimSpace(next.Description) == "" {
		t.Error("next_task description must not be empty")
	}
	nextArgs := make(map[string]mcpgo.PromptArgument)
	for _, a := range next.Arguments {
		nextArgs[a.Name] = a
	}
	if _, ok := nextArgs["project_id"]; !ok {
		t.Error("next_task missing project_id argument schema")
	}
	if _, ok := nextArgs["assignee"]; !ok {
		t.Error("next_task missing assignee argument schema")
	}
	if _, ok := nextArgs["actor_name"]; !ok {
		t.Error("next_task missing actor_name argument schema")
	}
}

// TestNextTask_GuidancePlanFromLiveTool validates Acceptance Criterion 2:
// "Invoking the prompt returns guidance/plan built from live tool results (e.g., next unblocked task by priority/due)."
func TestNextTask_GuidancePlanFromLiveTool(t *testing.T) {
	dueAt := "2026-10-01T12:00:00Z"
	expiresAt := "2026-09-27T23:00:00Z"
	completedAt := "2026-09-27T18:00:00Z"

	mock := &mockClient{
		listTasksFunc: func(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error) {
			if f.Status == "todo" && f.Unblocked {
				return &apiclient.TaskList{
					Tasks: []apiclient.TaskSummary{
						{
							ID:        "TASK-TOP-PRIORITY",
							Title:     "Deploy MCP Prompts",
							Status:    "todo",
							Priority:  4,
							DueAt:     &dueAt,
							Version:   3,
							CreatedAt: "2026-09-27T10:00:00Z",
							UpdatedAt: "2026-09-27T10:00:00Z",
						},
						{
							ID:        "TASK-LOWER-PRIORITY",
							Title:     "Minor doc update",
							Status:    "todo",
							Priority:  1,
							Version:   1,
							CreatedAt: "2026-09-27T11:00:00Z",
							UpdatedAt: "2026-09-27T11:00:00Z",
						},
					},
					Total: 2,
				}, nil
			}
			return &apiclient.TaskList{}, nil
		},
		getTaskFunc: func(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error) {
			if id != "TASK-TOP-PRIORITY" {
				t.Fatalf("unexpected GetTask ID: %s", id)
			}
			return &apiclient.TaskDetail{
				TaskSummary: apiclient.TaskSummary{
					ID:             id,
					Title:          "Deploy MCP Prompts",
					Status:         "todo",
					Priority:       4,
					DueAt:          &dueAt,
					Version:        3,
					Assignee:       &apiclient.Actor{Type: "agent", Name: "agent-mcp"},
					ClaimedBy:      &apiclient.Actor{Type: "agent", Name: "agent-mcp"},
					ClaimExpiresAt: &expiresAt,
					CreatedAt:      "2026-09-27T10:00:00Z",
					UpdatedAt:      "2026-09-27T10:00:00Z",
				},
				ProjectID:   "PROJ-ALPHA",
				Description: "Expose MCP prompts triage_open_tasks and next_task for agent workflows.",
				Notes:       "Previous agent investigated architecture and approved design.",
				AcceptanceCriteria: []apiclient.AcceptanceCriterion{
					{
						Seq:         1,
						Text:        "Discoverable via prompts/list",
						Done:        true,
						Note:        "Verified schema",
						CompletedBy: &apiclient.Actor{Type: "agent", Name: "agent-mcp"},
						CompletedAt: &completedAt,
					},
					{
						Seq:  2,
						Text: "Returns live guidance plan",
						Done: false,
					},
				},
				Comments: []apiclient.Comment{
					{
						ID:        "COMM-1",
						Kind:      "progress",
						Body:      "Starting prompt implementation",
						Actor:     apiclient.Actor{Type: "agent", Name: "agent-mcp"},
						CreatedAt: "2026-09-27T10:30:00Z",
					},
				},
			}, nil
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	defaultActor := apiclient.Actor{Type: "agent", Name: "agent-mcp"}
	RegisterPrompts(mcs, mock, defaultActor)

	getMsg := `{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "prompts/get",
		"params": {
			"name": "next_task",
			"arguments": {
				"project_id": "PROJ-ALPHA",
				"assignee": "agent-mcp",
				"actor_name": "agent-mcp"
			}
		}
	}`

	resp := mcs.HandleMessage(context.Background(), json.RawMessage(getMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	res, ok := jsonResp.Result.(mcpgo.GetPromptResult)
	if !ok {
		t.Fatalf("expected GetPromptResult, got %T: %+v", jsonResp.Result, jsonResp.Result)
	}

	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}

	txtContent, ok := res.Messages[0].Content.(mcpgo.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Messages[0].Content)
	}
	body := txtContent.Text

	// Verify live task details are included in the prompt
	expectedSubstrings := []string{
		"TASK-TOP-PRIORITY",
		"Deploy MCP Prompts",
		"PROJ-ALPHA",
		"Priority**: 4",
		"Expose MCP prompts triage_open_tasks and next_task",
		"Previous agent investigated architecture",
		"Seq 1**: Discoverable via prompts/list",
		"Seq 2**: Returns live guidance plan",
		"Starting prompt implementation",
		"claim_task(id=\"TASK-TOP-PRIORITY\", actor={\"type\": \"agent\", \"name\": \"agent-mcp\"})",
		"Attach Testing Evidence",
		"attach_artifact(id=\"TASK-TOP-PRIORITY\", kind=\"command-output\"",
		"release_task(id=\"TASK-TOP-PRIORITY\"",
		"update_task(id=\"TASK-TOP-PRIORITY\", version=<version from the release response>, status=\"done\"",
		"heartbeat_task(id=\"TASK-TOP-PRIORITY\"",
		"complete_ac(id=\"TASK-TOP-PRIORITY\", seq=1",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(body, sub) {
			t.Errorf("prompt body missing expected content: %q\nFull body:\n%s", sub, body)
		}
	}
}

// TestPrompts_GracefulEmptyDataFallback validates Acceptance Criterion 3:
// "Prompts degrade gracefully on empty data — a clear message, not a crash."
func TestPrompts_GracefulEmptyDataFallback(t *testing.T) {
	mockEmpty := &mockClient{
		listTasksFunc: func(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error) {
			return &apiclient.TaskList{Tasks: []apiclient.TaskSummary{}, Total: 0}, nil
		},
		listProjectsFunc: func(ctx context.Context) (*apiclient.ProjectList, error) {
			return &apiclient.ProjectList{Projects: []apiclient.Project{}, Total: 0}, nil
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	defaultActor := apiclient.Actor{Type: "agent", Name: "agent-mcp"}
	RegisterPrompts(mcs, mockEmpty, defaultActor)

	// 1. next_task on empty data
	nextMsg := `{
		"jsonrpc": "2.0",
		"id": 10,
		"method": "prompts/get",
		"params": {
			"name": "next_task",
			"arguments": {
				"project_id": "NON-EXISTENT"
			}
		}
	}`
	resp := mcs.HandleMessage(context.Background(), json.RawMessage(nextMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	res, ok := jsonResp.Result.(mcpgo.GetPromptResult)
	if !ok {
		t.Fatalf("expected GetPromptResult, got %T", jsonResp.Result)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	txtContent := res.Messages[0].Content.(mcpgo.TextContent)
	if !strings.Contains(txtContent.Text, "No unblocked tasks are currently available") {
		t.Errorf("expected graceful fallback message, got: %s", txtContent.Text)
	}
	if !strings.Contains(txtContent.Text, "create_task") {
		t.Errorf("expected recommendation to create_task, got: %s", txtContent.Text)
	}

	// 2. triage_open_tasks on empty data
	triageMsg := `{
		"jsonrpc": "2.0",
		"id": 11,
		"method": "prompts/get",
		"params": {
			"name": "triage_open_tasks",
			"arguments": {
				"project_id": "NON-EXISTENT"
			}
		}
	}`
	resp = mcs.HandleMessage(context.Background(), json.RawMessage(triageMsg))
	jsonResp, ok = resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	res, ok = jsonResp.Result.(mcpgo.GetPromptResult)
	if !ok {
		t.Fatalf("expected GetPromptResult, got %T", jsonResp.Result)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	txtContent = res.Messages[0].Content.(mcpgo.TextContent)
	if !strings.Contains(txtContent.Text, "No open tasks were found") {
		t.Errorf("expected graceful fallback message, got: %s", txtContent.Text)
	}
	if !strings.Contains(txtContent.Text, "create_task") {
		t.Errorf("expected next action suggestion, got: %s", txtContent.Text)
	}
}

// TestTriageOpenTasks_TriageSummary tests triage breakdown with doing, ready, and blocked tasks.
func TestTriageOpenTasks_TriageSummary(t *testing.T) {
	expiresAt := "2026-09-28T00:00:00Z"
	mock := &mockClient{
		listProjectsFunc: func(ctx context.Context) (*apiclient.ProjectList, error) {
			return &apiclient.ProjectList{
				Projects: []apiclient.Project{
					{ID: "PROJ-1", Name: "Core Engine"},
				},
				Total: 1,
			}, nil
		},
		listTasksFunc: func(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error) {
			switch {
			case f.Status == "doing":
				return &apiclient.TaskList{
					Tasks: []apiclient.TaskSummary{
						{
							ID:             "TASK-DOING-1",
							Title:          "Database migration",
							Status:         "doing",
							Priority:       5,
							ClaimedBy:      &apiclient.Actor{Type: "agent", Name: "migrator"},
							ClaimExpiresAt: &expiresAt,
						},
					},
					Total: 1,
				}, nil
			case f.Status == "todo" && f.Unblocked:
				return &apiclient.TaskList{
					Tasks: []apiclient.TaskSummary{
						{
							ID:       "TASK-READY-1",
							Title:    "Write tests",
							Status:   "todo",
							Priority: 3,
						},
					},
					Total: 1,
				}, nil
			case f.Status == "todo":
				return &apiclient.TaskList{
					Tasks: []apiclient.TaskSummary{
						{
							ID:       "TASK-READY-1",
							Title:    "Write tests",
							Status:   "todo",
							Priority: 3,
						},
						{
							ID:       "TASK-BLOCKED-1",
							Title:    "Release build",
							Status:   "todo",
							Priority: 4,
						},
					},
					Total: 2,
				}, nil
			default:
				return &apiclient.TaskList{}, nil
			}
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}
	RegisterPrompts(mcs, mock, defaultActor)

	triageMsg := `{
		"jsonrpc": "2.0",
		"id": 20,
		"method": "prompts/get",
		"params": {
			"name": "triage_open_tasks",
			"arguments": {
				"project_id": "PROJ-1"
			}
		}
	}`

	resp := mcs.HandleMessage(context.Background(), json.RawMessage(triageMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	res, ok := jsonResp.Result.(mcpgo.GetPromptResult)
	if !ok {
		t.Fatalf("expected GetPromptResult, got %T", jsonResp.Result)
	}

	txtContent := res.Messages[0].Content.(mcpgo.TextContent)
	body := txtContent.Text

	expected := []string{
		"Core Engine",
		"PROJ-1",
		"Total Open Tasks**: 3",
		"In-Progress (`doing`)**: 1",
		"Ready for Pickup (`todo`, unblocked)**: 1",
		"Blocked (`todo`, has unresolved blockers)**: 1",
		"TASK-DOING-1",
		"Database migration",
		"claimed by migrator",
		"TASK-READY-1",
		"Write tests",
		"TASK-BLOCKED-1",
		"Release build",
		"Pick up the highest priority ready task",
	}

	for _, exp := range expected {
		if !strings.Contains(body, exp) {
			t.Errorf("triage body missing expected string %q\nFull body:\n%s", exp, body)
		}
	}
}

// TestPrompts_UpstreamErrorDegradesGracefully verifies errors do not panic or crash.
func TestPrompts_UpstreamErrorDegradesGracefully(t *testing.T) {
	mockErr := &mockClient{
		listTasksFunc: func(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error) {
			return nil, errors.New("connection refused: 127.0.0.1:8080")
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}
	RegisterPrompts(mcs, mockErr, defaultActor)

	getMsg := `{
		"jsonrpc": "2.0",
		"id": 30,
		"method": "prompts/get",
		"params": {
			"name": "next_task"
		}
	}`
	resp := mcs.HandleMessage(context.Background(), json.RawMessage(getMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	res, ok := jsonResp.Result.(mcpgo.GetPromptResult)
	if !ok {
		t.Fatalf("expected GetPromptResult, got %T", jsonResp.Result)
	}
	txt := res.Messages[0].Content.(mcpgo.TextContent).Text
	if !strings.Contains(txt, "connection refused") {
		t.Errorf("expected error message to be reported in prompt content, got: %s", txt)
	}
}
