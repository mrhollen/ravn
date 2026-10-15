package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkAddRelation measures the throughput of adding a relation to a task
// with many child collections (comments, ACs, artifacts, events). Because
// handleAddRelation uses the use-case outcome indicator instead of calling
// Tasks.GetDetail, it avoids loading these collections on every request.
func BenchmarkAddRelation(b *testing.B) {
	stk := newStack(b)
	p := createProject(b, stk, "Bench Project")
	task := newTask(b, stk, p.ID, "Target Task")

	// Pre-populate with comments, ACs, and artifacts
	for i := 1; i <= 20; i++ {
		doReq(b, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
			"actor": actor("bench"), "content": fmt.Sprintf("Comment %d", i),
		})
		doReq(b, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
			"actor": actor("bench"), "text": fmt.Sprintf("AC %d", i),
		})
		doReq(b, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
			"actor": actor("bench"), "kind": "log", "ref": fmt.Sprintf("log%d.txt", i), "summary": "log",
		})
	}

	other := newTask(b, stk, p.ID, "Other Task")
	body, _ := json.Marshal(map[string]any{
		"actor":    actor("bench"),
		"other_id": other.ID,
		"kind":     "related",
	})

	path := "/api/v1/tasks/" + task.ID + "/relations"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		stk.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			b.Fatalf("status = %d, want 200 or 201", rec.Code)
		}
	}
}
