package httpapi

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

// uploadArtifactFile posts a small file through the MULTIPART upload path (form field
// "file") so the artifact has real bytes in ArtifactsDir — GET /artifacts/{id} serves
// uploaded files, while JSON-attach artifacts only record their ref. The acting name is
// taken from the actor_name form field (see handleUploadArtifact).
func uploadArtifactFile(t *testing.T, h http.Handler, path, filename string, content []byte, actorName, bearer string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("actor_name", actorName); err != nil {
		t.Fatalf("write actor_name: %v", err)
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://ravn.test"+path, &buf)
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

// scoping_adversarial_test.go: hostile-input tests for the T4 ?mine= parsing and
// the artifact read-path gate — per repo convention these live in *_adversarial_test.go.

func TestParseMine_Adversarial(t *testing.T) {
	cases := []struct {
		name    string
		raw     string // query as received ("" = no query at all)
		want    string
		wantErr bool
	}{
		{"absent", "", "", false},
		{"empty-value", "mine=", "", false},
		{"multiple-empty-values", "mine=&mine=", "", false},
		{"single", "mine=created", "created", false},
		{"padded-single", "mine=%20%20created%20", "created", false},
		{"duplicate-same-value", "mine=created&mine=created", "created", false},
		{"duplicate-padded-and-bare", "mine=created&mine=%20created", "created", false},
		{"unicode-nbsp-padding", "mine=\u00a0assigned\u00a0", "assigned", false}, // NBSP IS unicode whitespace: TrimSpace strips it
		{"two-distinct-values", "mine=assigned&mine=created", "", true},
		{"three-distinct-values", "mine=a&mine=b&mine=c", "", true},
		{"one-distinct-among-dups", "mine=a&mine=a&mine=b", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := url.Values{}
			if c.raw != "" {
				parsed, err := url.ParseQuery(c.raw)
				if err != nil {
					t.Fatalf("ParseQuery(%q): %v", c.raw, err)
				}
				q = parsed
			}
			got, err := parseMine(q)
			if (err != nil) != c.wantErr {
				t.Fatalf("parseMine(%q) err = %v, wantErr=%v", c.raw, err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("parseMine(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestParseMine_HostileLargeInput(t *testing.T) {
	// Thousands of repeated values: must collapse/decide in linear time and stay correct.
	q := url.Values{}
	for i := 0; i < 10_000; i++ {
		q.Add("mine", fmt.Sprintf("%d", i%2)) // only "0" and "1" distinct → error, fast
	}
	if got, err := parseMine(q); err == nil || got != "" {
		t.Fatalf("parseMine(10k values) = (%q, %v), want (\"\", error)", got, err)
	}
	q = url.Values{}
	for i := 0; i < 10_000; i++ {
		q.Add("mine", "created")
	}
	got, err := parseMine(q)
	if err != nil || got != "created" {
		t.Fatalf("parseMine(10k duplicates) = (%q, %v), want (\"created\", nil)", got, err)
	}
}

// TestScoping_ArtifactGate_Adversarial pins the hostile cases on GET/DELETE
// /artifacts under membership scoping: an artifact of an INVISIBLE task must be
// rejected BEFORE its file is ever read or deleted (403), and a nonexistent id
// is a 404 — never a 500 from the ownership walk.
func TestScoping_ArtifactGate_Adversarial(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")
	op := scopingSharedToken

	// Operator-owned unattributed project + task + url artifact (no file I/O).
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Secret"}), op)
	if code != 201 {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{"title": "t", "project_id": pid}), op)
	if code != 201 {
		t.Fatalf("create task = %d: %s", code, body)
	}
	tid := extractID(t, body)
	code, body = uploadArtifactFile(t, stk.Handler, "/api/v1/tasks/"+tid+"/artifacts", "evidence.txt", []byte("evidence-bytes"), "operator", op)
	if code != 201 {
		t.Fatalf("upload artifact = %d: %s", code, body)
	}
	aid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/artifacts/" + aid},
		{"DELETE", "/api/v1/tasks/" + tid + "/artifacts/" + aid + "?actor_name=bob"},
	} {
		code, body = authReq(t, stk.Handler, c.method, c.path, nil, tokB)
		if code != 403 {
			t.Fatalf("%s %s (invisible task) = %d, want 403: %s", c.method, c.path, code, body)
		}
		forbiddenBody(t, body, "project", "viewer") // read denial surfaces the required role; DELETE's gateTask is OpDestructive but visibility fails first
	}

	// Nonexistent id under membership: clean 404 from the ownership walk.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/artifacts/07ZZZZZZZZZZZZZZZZZZZZZZZZZ", nil, tokB)
	if code != 404 {
		t.Fatalf("GET unknown artifact (membership) = %d, want 404: %s", code, body)
	}

	// Operator bypass still works on the same artifact.
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/artifacts/"+aid, nil, op)
	if code != 200 {
		t.Fatalf("operator GET artifact = %d, want 200", code)
	}

	// And a MEMBER of the project may read it (gate passes, file served).
	seedMember(t, stk, domain.ID(pid), "bob", "viewer")
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/artifacts/"+aid, nil, tokB)
	if code != 200 {
		t.Fatalf("member GET artifact = %d, want 200: %s", code, body)
	}
}
