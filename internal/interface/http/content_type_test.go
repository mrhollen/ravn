package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

func strPtr(s string) *string {
	return &s
}

func doCustomReq(h http.Handler, method, path string, contentType *string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, "http://ravn.test"+path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "http://ravn.test"+path, nil)
	}
	if contentType != nil {
		req.Header.Set("Content-Type", *contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestContentTypeEnforcement_MutatingEndpoints tests AC 1:
// POST, PUT, and PATCH endpoints verify Content-Type contains application/json,
// returning HTTP 415 Unsupported Media Type when invalid or missing on non-empty bodies.
func TestContentTypeEnforcement_MutatingEndpoints(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	proj := createProject(t, stk, "CT Enforcement Proj")
	task := newTask(t, stk, proj.ID, "CT Enforcement Task")
	task2 := newTask(t, stk, proj.ID, "CT Enforcement Task 2")

	// Add an AC for toggle testing
	code, acBody := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
		"actor": actor("ada"), "text": "First AC",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup AC failed: %d", code)
	}
	var ac domain.AcceptanceCriterion
	if err := json.Unmarshal(acBody, &ac); err != nil {
		t.Fatalf("unmarshal AC: %v", err)
	}

	endpoints := []struct {
		name       string
		method     string
		path       string
		validBody  string
		wantStatus int // expected status on valid Content-Type
	}{
		{
			name:       "POST /projects",
			method:     "POST",
			path:       "/api/v1/projects",
			validBody:  `{"actor":{"name":"ada","type":"human"},"name":"New Proj","description":"desc"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "PUT /projects/{id}",
			method:     "PUT",
			path:       "/api/v1/projects/" + proj.ID,
			validBody:  `{"actor":{"name":"ada","type":"human"},"name":"Renamed Proj"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks",
			method:     "POST",
			path:       "/api/v1/tasks",
			validBody:  `{"actor":{"name":"ada","type":"human"},"project_id":"` + proj.ID + `","title":"Another Task"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "PATCH /tasks/{id}",
			method:     "PATCH",
			path:       "/api/v1/tasks/" + task.ID,
			validBody:  `{"actor":{"name":"ada","type":"human"},"version":1,"title":"Patched Title"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/{id}/claim",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/claim",
			validBody:  `{"actor":{"name":"ada","type":"human"}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/{id}/release",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/release",
			validBody:  `{"actor":{"name":"ada","type":"human"},"note":"releasing"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/{id}/comments",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/comments",
			validBody:  `{"actor":{"name":"ada","type":"human"},"kind":"progress","body":"making progress"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "PUT /tasks/{id}/notes",
			method:     "PUT",
			path:       "/api/v1/tasks/" + task.ID + "/notes",
			validBody:  `{"actor":{"name":"ada","type":"human"},"notes":"some notes"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/{id}/acceptance-criteria",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/acceptance-criteria",
			validBody:  `{"actor":{"name":"ada","type":"human"},"text":"Second AC"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "PATCH /tasks/{id}/acceptance-criteria/1",
			method:     "PATCH",
			path:       "/api/v1/tasks/" + task.ID + "/acceptance-criteria/1",
			validBody:  `{"actor":{"name":"ada","type":"human"},"done":true}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/{id}/artifacts",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/artifacts",
			validBody:  `{"actor":{"name":"ada","type":"human"},"kind":"url","ref":"https://example.com","summary":"proof"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "POST /tasks/{id}/relations",
			method:     "POST",
			path:       "/api/v1/tasks/" + task.ID + "/relations",
			validBody:  `{"actor":{"name":"ada","type":"human"},"other_id":"` + task2.ID + `","kind":"related"}`,
			wantStatus: http.StatusCreated,
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name+" missing Content-Type rejects with 415", func(t *testing.T) {
			rec := doCustomReq(stk.Handler, ep.method, ep.path, nil, ep.validBody)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s without Content-Type: code = %d, want 415 (body: %s)", ep.method, ep.path, rec.Code, rec.Body.String())
			}
			var res errResp
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal error resp: %v", err)
			}
			if res.Error != "unsupported media type" {
				t.Errorf("error = %q, want 'unsupported media type'", res.Error)
			}
		})

		t.Run(ep.name+" text/plain rejects with 415", func(t *testing.T) {
			rec := doCustomReq(stk.Handler, ep.method, ep.path, strPtr("text/plain"), ep.validBody)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s with text/plain: code = %d, want 415 (body: %s)", ep.method, ep.path, rec.Code, rec.Body.String())
			}
			var res errResp
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal error resp: %v", err)
			}
			if res.Error != "unsupported media type" {
				t.Errorf("error = %q, want 'unsupported media type'", res.Error)
			}
		})

		t.Run(ep.name+" form-urlencoded rejects with 415", func(t *testing.T) {
			rec := doCustomReq(stk.Handler, ep.method, ep.path, strPtr("application/x-www-form-urlencoded"), ep.validBody)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s with form-urlencoded: code = %d, want 415 (body: %s)", ep.method, ep.path, rec.Code, rec.Body.String())
			}
			var res errResp
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal error resp: %v", err)
			}
			if res.Error != "unsupported media type" {
				t.Errorf("error = %q, want 'unsupported media type'", res.Error)
			}
		})

		t.Run(ep.name+" application/json; charset=utf-8 accepted", func(t *testing.T) {
			rec := doCustomReq(stk.Handler, ep.method, ep.path, strPtr("application/json; charset=utf-8"), ep.validBody)
			// Status should not be 415 (may succeed or hit state check, but Content-Type was accepted)
			if rec.Code == http.StatusUnsupportedMediaType {
				t.Errorf("%s %s with application/json; charset=utf-8 got unexpected 415", ep.method, ep.path)
			}
		})
	}
}

// TestDisallowUnknownFields_RejectsMisspelledOrUnknownFields tests AC 2:
// decodeJSON uses decoder.DisallowUnknownFields() to reject misspelled or unknown properties with 400 Bad Request.
func TestDisallowUnknownFields_RejectsMisspelledOrUnknownFields(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	proj := createProject(t, stk, "DUF Test Proj")
	task := newTask(t, stk, proj.ID, "DUF Test Task")
	task2 := newTask(t, stk, proj.ID, "DUF Test Task 2")

	cases := []struct {
		name         string
		method       string
		path         string
		payload      string
		unknownField string
	}{
		{
			name:         "POST /projects unknown field at root",
			method:       "POST",
			path:         "/api/v1/projects",
			payload:      `{"actor":{"name":"ada","type":"human"},"name":"Proj","description":"desc","extra_field":"bad"}`,
			unknownField: "extra_field",
		},
		{
			name:         "POST /projects misspelled property",
			method:       "POST",
			path:         "/api/v1/projects",
			payload:      `{"actor":{"name":"ada","type":"human"},"name":"Proj","descrption":"typo"}`,
			unknownField: "descrption",
		},
		{
			name:         "POST /projects unknown field in nested actor",
			method:       "POST",
			path:         "/api/v1/projects",
			payload:      `{"actor":{"name":"ada","type":"human","role":"admin"},"name":"Proj"}`,
			unknownField: "role",
		},
		{
			name:         "PUT /projects/{id} unknown field",
			method:       "PUT",
			path:         "/api/v1/projects/" + proj.ID,
			payload:      `{"actor":{"name":"ada","type":"human"},"name":"Proj","bogus":123}`,
			unknownField: "bogus",
		},
		{
			name:         "POST /tasks misspelled priority",
			method:       "POST",
			path:         "/api/v1/tasks",
			payload:      `{"actor":{"name":"ada","type":"human"},"project_id":"` + proj.ID + `","title":"Task","proirity":3}`,
			unknownField: "proirity",
		},
		{
			name:         "POST /tasks misspelled assignee",
			method:       "POST",
			path:         "/api/v1/tasks",
			payload:      `{"actor":{"name":"ada","type":"human"},"project_id":"` + proj.ID + `","title":"Task","assigne":{"name":"bob"}}`,
			unknownField: "assigne",
		},
		{
			name:         "POST /tasks misspelled due_date instead of due_at",
			method:       "POST",
			path:         "/api/v1/tasks",
			payload:      `{"actor":{"name":"ada","type":"human"},"project_id":"` + proj.ID + `","title":"Task","due_date":"2026-12-01T00:00:00Z"}`,
			unknownField: "due_date",
		},
		{
			name:         "PATCH /tasks/{id} unknown field",
			method:       "PATCH",
			path:         "/api/v1/tasks/" + task.ID,
			payload:      `{"actor":{"name":"ada","type":"human"},"version":1,"staus":"done"}`,
			unknownField: "staus",
		},
		{
			name:         "POST /tasks/{id}/claim misspelled ttl_seconds instead of ttl_sec",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/claim",
			payload:      `{"actor":{"name":"ada","type":"human"},"ttl_seconds":1800}`,
			unknownField: "ttl_seconds",
		},
		{
			name:         "POST /tasks/{id}/release misspelled note",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/release",
			payload:      `{"actor":{"name":"ada","type":"human"},"notte":"done releasing"}`,
			unknownField: "notte",
		},
		{
			name:         "POST /tasks/{id}/heartbeat unknown field",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/heartbeat",
			payload:      `{"actor":{"name":"ada","type":"human"},"extra":"value"}`,
			unknownField: "extra",
		},
		{
			name:         "POST /tasks/{id}/comments unknown field",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/comments",
			payload:      `{"actor":{"name":"ada","type":"human"},"kind":"progress","body":"test","pinned":true}`,
			unknownField: "pinned",
		},
		{
			name:         "PUT /tasks/{id}/notes unknown field",
			method:       "PUT",
			path:         "/api/v1/tasks/" + task.ID + "/notes",
			payload:      `{"actor":{"name":"ada","type":"human"},"notes":"scratch","markdown":true}`,
			unknownField: "markdown",
		},
		{
			name:         "POST /tasks/{id}/acceptance-criteria misspelled text",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/acceptance-criteria",
			payload:      `{"actor":{"name":"ada","type":"human"},"txt":"criterion"}`,
			unknownField: "txt",
		},
		{
			name:         "PATCH /tasks/{id}/acceptance-criteria/1 unknown field",
			method:       "PATCH",
			path:         "/api/v1/tasks/" + task.ID + "/acceptance-criteria/1",
			payload:      `{"actor":{"name":"ada","type":"human"},"done":true,"status":"complete"}`,
			unknownField: "status",
		},
		{
			name:         "POST /tasks/{id}/artifacts misspelled kind",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/artifacts",
			payload:      `{"actor":{"name":"ada","type":"human"},"type":"url","ref":"https://example.com"}`,
			unknownField: "type",
		},
		{
			name:         "POST /tasks/{id}/relations misspelled other_id",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/relations",
			payload:      `{"actor":{"name":"ada","type":"human"},"target_id":"` + task2.ID + `","kind":"related"}`,
			unknownField: "target_id",
		},
		{
			name:         "POST /tasks/{id}/opencode/start unknown field",
			method:       "POST",
			path:         "/api/v1/tasks/" + task.ID + "/opencode/start",
			payload:      `{"unknown_setting":"invalid"}`,
			unknownField: "unknown_setting",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doCustomReq(stk.Handler, tc.method, tc.path, strPtr("application/json"), tc.payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want 400 (body: %s)", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			var res errResp
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal error resp: %v", err)
			}
			if res.Error != "invalid input" {
				t.Errorf("error = %q, want 'invalid input'", res.Error)
			}
			expectedMsgPart := `unknown field "` + tc.unknownField + `"`
			if !strings.Contains(res.Message, expectedMsgPart) {
				t.Errorf("message %q does not contain %q", res.Message, expectedMsgPart)
			}
		})
	}
}

// TestProperlyFormedJSONPayloadsDecodeSeamlessly tests AC 3:
// Properly formed JSON payloads with valid keys continue to decode without issue.
func TestProperlyFormedJSONPayloadsDecodeSeamlessly(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	// 1. POST /projects
	projRec := doCustomReq(stk.Handler, "POST", "/api/v1/projects", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"name":"Valid Proj","description":"A valid project"}`)
	if projRec.Code != http.StatusCreated {
		t.Fatalf("POST /projects: %d (body %s)", projRec.Code, projRec.Body.String())
	}
	var proj projectDTO
	if err := json.Unmarshal(projRec.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if proj.Name != "Valid Proj" {
		t.Errorf("project name = %q, want 'Valid Proj'", proj.Name)
	}

	// 2. PUT /projects/{id}
	putProjRec := doCustomReq(stk.Handler, "PUT", "/api/v1/projects/"+proj.ID, strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"description":"Updated description"}`)
	if putProjRec.Code != http.StatusOK {
		t.Fatalf("PUT /projects: %d (body %s)", putProjRec.Code, putProjRec.Body.String())
	}

	// 3. POST /tasks
	taskRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"project_id":"`+proj.ID+`","title":"Valid Task","priority":2,"acceptance_criteria":["AC1","AC2"]}`)
	if taskRec.Code != http.StatusCreated {
		t.Fatalf("POST /tasks: %d (body %s)", taskRec.Code, taskRec.Body.String())
	}
	var createdTask createTaskResponse
	if err := json.Unmarshal(taskRec.Body.Bytes(), &createdTask); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID := createdTask.Task.ID

	// 4. PATCH /tasks/{id}
	patchRec := doCustomReq(stk.Handler, "PATCH", "/api/v1/tasks/"+taskID, strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"version":1,"priority":4}`)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH /tasks: %d (body %s)", patchRec.Code, patchRec.Body.String())
	}

	// 5. POST /tasks/{id}/claim
	claimRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/claim", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"ttl_sec":600}`)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("POST claim: %d (body %s)", claimRec.Code, claimRec.Body.String())
	}

	// 6. POST /tasks/{id}/heartbeat
	hbRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/heartbeat", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"}}`)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("POST heartbeat: %d (body %s)", hbRec.Code, hbRec.Body.String())
	}

	// 7. POST /tasks/{id}/comments
	cmtRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/comments", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"kind":"progress","body":"All going well"}`)
	if cmtRec.Code != http.StatusCreated {
		t.Fatalf("POST comment: %d (body %s)", cmtRec.Code, cmtRec.Body.String())
	}

	// 8. PUT /tasks/{id}/notes
	notesRec := doCustomReq(stk.Handler, "PUT", "/api/v1/tasks/"+taskID+"/notes", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"notes":"Detailed progress notes"}`)
	if notesRec.Code != http.StatusOK {
		t.Fatalf("PUT notes: %d (body %s)", notesRec.Code, notesRec.Body.String())
	}

	// 9. POST /tasks/{id}/acceptance-criteria
	acRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/acceptance-criteria", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"text":"New acceptance criterion"}`)
	if acRec.Code != http.StatusCreated {
		t.Fatalf("POST AC: %d (body %s)", acRec.Code, acRec.Body.String())
	}
	var newAC domain.AcceptanceCriterion
	if err := json.Unmarshal(acRec.Body.Bytes(), &newAC); err != nil {
		t.Fatalf("decode AC: %v", err)
	}

	// 10. PATCH /tasks/{id}/acceptance-criteria/{seq}
	toggleRec := doCustomReq(stk.Handler, "PATCH", "/api/v1/tasks/"+taskID+"/acceptance-criteria/1", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"done":true,"note":"verified locally"}`)
	if toggleRec.Code != http.StatusOK {
		t.Fatalf("PATCH AC: %d (body %s)", toggleRec.Code, toggleRec.Body.String())
	}

	// 11. POST /tasks/{id}/artifacts
	artRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/artifacts", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"kind":"url","ref":"https://ci.example.com/build/1","summary":"CI run"}`)
	if artRec.Code != http.StatusCreated {
		t.Fatalf("POST artifact: %d (body %s)", artRec.Code, artRec.Body.String())
	}

	// 12. POST /tasks/{id}/relations
	t2 := newTask(t, stk, proj.ID, "Related Task")
	relRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/relations", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"other_id":"`+t2.ID+`","kind":"related"}`)
	if relRec.Code != http.StatusCreated {
		t.Fatalf("POST relation: %d (body %s)", relRec.Code, relRec.Body.String())
	}

	// 13. POST /tasks/{id}/release
	relTaskRec := doCustomReq(stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/release", strPtr("application/json"),
		`{"actor":{"name":"ada","type":"human"},"note":"done for now"}`)
	if relTaskRec.Code != http.StatusOK {
		t.Fatalf("POST release: %d (body %s)", relTaskRec.Code, relTaskRec.Body.String())
	}
}
