package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// TestResourcesList_Discoverability validates:
// 1. resources/list does NOT advertise parameterized template URIs containing curly braces.
// 2. Parameterized URIs are registered exclusively as ResourceTemplates in resources/templates/list.
// 3. Any resources listed in resources/list are concrete and readable without errors.
func TestResourcesList_Discoverability(t *testing.T) {
	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	mock := &mockClient{}
	RegisterResources(mcs, mock)

	// 1. Check resources/list: must not advertise parameterized template URIs
	listMsg := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
	resp := mcs.HandleMessage(context.Background(), json.RawMessage(listMsg))

	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	res, ok := jsonResp.Result.(mcpgo.ListResourcesResult)
	if !ok {
		t.Fatalf("expected ListResourcesResult, got %T: %+v", jsonResp.Result, jsonResp.Result)
	}

	for _, r := range res.Resources {
		if strings.Contains(r.URI, "{") || strings.Contains(r.URI, "}") {
			t.Errorf("resources/list must not contain parameterized template URIs, got: %q", r.URI)
		}
		// Any advertised resource must be readable
		readMsg := fmt.Sprintf(`{"jsonrpc":"2.0","id":99,"method":"resources/read","params":{"uri":%q}}`, r.URI)
		readResp := mcs.HandleMessage(context.Background(), json.RawMessage(readMsg))
		if _, ok := readResp.(mcpgo.JSONRPCResponse); !ok {
			t.Errorf("advertised resource %q failed to read: %+v", r.URI, readResp)
		}
	}

	// 2. Check resources/templates/list: parameterized URIs must be registered here
	tmplListMsg := `{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`
	resp = mcs.HandleMessage(context.Background(), json.RawMessage(tmplListMsg))

	jsonResp, ok = resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	tmplRes, ok := jsonResp.Result.(mcpgo.ListResourceTemplatesResult)
	if !ok {
		t.Fatalf("expected ListResourceTemplatesResult, got %T: %+v", jsonResp.Result, jsonResp.Result)
	}

	var foundTaskTemplate, foundProjectTemplate bool
	for _, tmpl := range tmplRes.ResourceTemplates {
		if tmpl.URITemplate != nil {
			if tmpl.URITemplate.Raw() == "crowban://task/{id}" {
				foundTaskTemplate = true
			}
			if tmpl.URITemplate.Raw() == "crowban://project/{id}" {
				foundProjectTemplate = true
			}
		}
	}
	if !foundTaskTemplate {
		t.Errorf("expected crowban://task/{id} in resources/templates/list, got: %+v", tmplRes.ResourceTemplates)
	}
	if !foundProjectTemplate {
		t.Errorf("expected crowban://project/{id} in resources/templates/list, got: %+v", tmplRes.ResourceTemplates)
	}
}

// TestResourcesRead_MatchesResumePayload validates Acceptance Criterion 2:
// "The resource body matches the get_task resume-payload shape."
func TestResourcesRead_MatchesResumePayload(t *testing.T) {
	dueAt := "2026-10-01T12:00:00Z"
	expiresAt := "2026-09-27T23:00:00Z"
	completedAt := "2026-09-27T18:00:00Z"

	expectedDetail := &apiclient.TaskDetail{
		TaskSummary: apiclient.TaskSummary{
			ID:             "01M3JAA9HK7AD45RWP0XADE3KB",
			Title:          "Fix DB race condition",
			Status:         "doing",
			Priority:       4,
			DueAt:          &dueAt,
			Assignee:       &apiclient.Actor{Type: "agent", Name: "agent-db"},
			ClaimedBy:      &apiclient.Actor{Type: "agent", Name: "agent-db"},
			ClaimExpiresAt: &expiresAt,
			Version:        3,
			CreatedAt:      "2026-09-27T10:00:00Z",
			UpdatedAt:      "2026-09-27T10:30:00Z",
		},
		ProjectID:   "01M3J9HHEGY26V7142C0603A3S",
		Description: "Resolve concurrent transaction conflict on sqlite leases.",
		Notes:       "Wal mode verified.",
		Archived:    false,
		Blocked:     false,
		Blockers:    []apiclient.Blocker{},
		AcceptanceCriteria: []apiclient.AcceptanceCriterion{
			{
				Seq:         1,
				Text:        "Handles 50 concurrent writers",
				Done:        true,
				Note:        "Load test passed",
				CompletedBy: &apiclient.Actor{Type: "agent", Name: "agent-db"},
				CompletedAt: &completedAt,
			},
		},
		Comments: []apiclient.Comment{
			{
				ID:        "COMM-1",
				Kind:      "progress",
				Body:      "Mutex lock added",
				Actor:     apiclient.Actor{Type: "agent", Name: "agent-db"},
				CreatedAt: "2026-09-27T10:15:00Z",
			},
		},
		Artifacts: []apiclient.Artifact{},
		Relations: []apiclient.Relation{},
		Events:    []apiclient.Event{},
	}

	mock := &mockClient{
		getTaskFunc: func(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error) {
			if id == "01M3JAA9HK7AD45RWP0XADE3KB" {
				return expectedDetail, nil
			}
			return nil, &apiclient.APIError{StatusCode: 404, Body: []byte(`{"error":"not_found","message":"task not found"}`)}
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	RegisterResources(mcs, mock)

	// Read via resource URI
	readMsg := `{
		"jsonrpc": "2.0",
		"id": 10,
		"method": "resources/read",
		"params": {
			"uri": "crowban://task/01M3JAA9HK7AD45RWP0XADE3KB"
		}
	}`

	resp := mcs.HandleMessage(context.Background(), json.RawMessage(readMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}

	readResult, ok := jsonResp.Result.(mcpgo.ReadResourceResult)
	if !ok {
		t.Fatalf("expected ReadResourceResult, got %T: %+v", jsonResp.Result, jsonResp.Result)
	}

	if len(readResult.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(readResult.Contents))
	}

	txtContent, ok := readResult.Contents[0].(mcpgo.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents, got %T", readResult.Contents[0])
	}

	if txtContent.URI != "crowban://task/01M3JAA9HK7AD45RWP0XADE3KB" {
		t.Errorf("expected URI crowban://task/01M3JAA9HK7AD45RWP0XADE3KB, got %q", txtContent.URI)
	}
	if txtContent.MIMEType != "application/json" {
		t.Errorf("expected MIMEType application/json, got %q", txtContent.MIMEType)
	}

	// Verify exact unmarshaling into TaskDetail mirrors the get_task resume-payload shape
	var decodedDetail apiclient.TaskDetail
	if err := json.Unmarshal([]byte(txtContent.Text), &decodedDetail); err != nil {
		t.Fatalf("failed to unmarshal resource body as TaskDetail: %v", err)
	}

	if decodedDetail.ID != expectedDetail.ID {
		t.Errorf("ID mismatch: got %q, want %q", decodedDetail.ID, expectedDetail.ID)
	}
	if decodedDetail.Title != expectedDetail.Title {
		t.Errorf("Title mismatch: got %q, want %q", decodedDetail.Title, expectedDetail.Title)
	}
	if decodedDetail.Status != expectedDetail.Status {
		t.Errorf("Status mismatch: got %q, want %q", decodedDetail.Status, expectedDetail.Status)
	}
	if decodedDetail.Priority != expectedDetail.Priority {
		t.Errorf("Priority mismatch: got %d, want %d", decodedDetail.Priority, expectedDetail.Priority)
	}
	if decodedDetail.Notes != expectedDetail.Notes {
		t.Errorf("Notes mismatch: got %q, want %q", decodedDetail.Notes, expectedDetail.Notes)
	}
	if len(decodedDetail.AcceptanceCriteria) != 1 || decodedDetail.AcceptanceCriteria[0].Text != "Handles 50 concurrent writers" {
		t.Errorf("AcceptanceCriteria mismatch: %+v", decodedDetail.AcceptanceCriteria)
	}
	if len(decodedDetail.Comments) != 1 || decodedDetail.Comments[0].Body != "Mutex lock added" {
		t.Errorf("Comments mismatch: %+v", decodedDetail.Comments)
	}
}

// TestResourcesRead_UnknownIDSurfacesError validates Acceptance Criterion 3:
// "An unknown id surfaces a clear MCP error, not an empty 200."
func TestResourcesRead_UnknownIDSurfacesError(t *testing.T) {
	mock := &mockClient{
		getTaskFunc: func(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error) {
			return nil, &apiclient.APIError{
				StatusCode: 404,
				Body:       []byte(`{"error":"not_found","message":"task not found: ` + id + `"}`),
			}
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	RegisterResources(mcs, mock)

	// Read non-existent task
	readMsg := `{
		"jsonrpc": "2.0",
		"id": 20,
		"method": "resources/read",
		"params": {
			"uri": "crowban://task/UNKNOWN_TASK_ID"
		}
	}`

	resp := mcs.HandleMessage(context.Background(), json.RawMessage(readMsg))

	// Must NOT be a successful JSONRPCResponse with empty content
	if _, ok := resp.(mcpgo.JSONRPCResponse); ok {
		t.Fatal("expected JSON-RPC error response for unknown ID, but received successful JSONRPCResponse (empty 200)")
	}

	errResp, ok := resp.(mcpgo.JSONRPCError)
	if !ok {
		t.Fatalf("expected JSONRPCErrorResponse, got %T: %+v", resp, resp)
	}

	if errResp.Error.Code == 0 {
		t.Error("expected non-zero error code")
	}
	if !strings.Contains(errResp.Error.Message, "task not found") && !strings.Contains(errResp.Error.Message, "UNKNOWN_TASK_ID") {
		t.Errorf("expected clear error message mentioning missing task, got: %s", errResp.Error.Message)
	}

	// Also verify reading literally crowban://task/{id} surfaces an error, not empty 200
	literalMsg := `{
		"jsonrpc": "2.0",
		"id": 21,
		"method": "resources/read",
		"params": {
			"uri": "crowban://task/{id}"
		}
	}`
	resp = mcs.HandleMessage(context.Background(), json.RawMessage(literalMsg))
	if _, ok := resp.(mcpgo.JSONRPCResponse); ok {
		t.Fatal("expected error when reading literal {id} without arguments, got success")
	}
	errResp, ok = resp.(mcpgo.JSONRPCError)
	if !ok {
		t.Fatalf("expected JSONRPCError, got %T", resp)
	}
	if !strings.Contains(errResp.Error.Message, "task ID required") && !strings.Contains(errResp.Error.Message, "not found") {
		t.Errorf("expected error message indicating missing ID or resource not found, got: %s", errResp.Error.Message)
	}
}

// TestProjectResourceRead tests reading crowban://project/{id}.
func TestProjectResourceRead(t *testing.T) {
	mock := &mockClient{
		listProjectsFunc: func(ctx context.Context) (*apiclient.ProjectList, error) {
			return &apiclient.ProjectList{
				Projects: []apiclient.Project{
					{
						ID:          "PROJ-1",
						Name:        "Core Engine",
						Description: "Engine work",
						TaskCounts:  &apiclient.StatusCounts{Todo: 5, Doing: 2, Done: 10},
					},
				},
				Total: 1,
			}, nil
		},
	}

	mcs := mcpsrv.NewMCPServer("crowban", "test-version")
	RegisterResources(mcs, mock)

	// 1. Success read
	readMsg := `{
		"jsonrpc": "2.0",
		"id": 30,
		"method": "resources/read",
		"params": {
			"uri": "crowban://project/PROJ-1"
		}
	}`
	resp := mcs.HandleMessage(context.Background(), json.RawMessage(readMsg))
	jsonResp, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	readResult, ok := jsonResp.Result.(mcpgo.ReadResourceResult)
	if !ok {
		t.Fatalf("expected ReadResourceResult, got %T", jsonResp.Result)
	}
	txtContent := readResult.Contents[0].(mcpgo.TextResourceContents)
	if !strings.Contains(txtContent.Text, "Core Engine") || !strings.Contains(txtContent.Text, `"todo":5`) {
		t.Errorf("project resource content missing expected fields: %s", txtContent.Text)
	}

	// 2. Unknown project surfaces error
	unknownMsg := `{
		"jsonrpc": "2.0",
		"id": 31,
		"method": "resources/read",
		"params": {
			"uri": "crowban://project/UNKNOWN-PROJECT"
		}
	}`
	resp = mcs.HandleMessage(context.Background(), json.RawMessage(unknownMsg))
	if _, ok := resp.(mcpgo.JSONRPCResponse); ok {
		t.Fatal("expected error for unknown project, got success")
	}
	errResp, ok := resp.(mcpgo.JSONRPCError)
	if !ok {
		t.Fatalf("expected JSONRPCErrorResponse, got %T", resp)
	}
	if !strings.Contains(errResp.Error.Message, "not found") {
		t.Errorf("expected not found error message, got: %s", errResp.Error.Message)
	}
}
