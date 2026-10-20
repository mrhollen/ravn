package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/ravn/internal/interface/http"
)

type testRig struct {
	server       *httptest.Server
	artifactsDir string
	db           *sql.DB
	app          *httpapi.App
	projectID    domain.ID
	taskID       domain.ID
}

func setupTestRig(t *testing.T) *testRig {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	artifactsDir := t.TempDir()
	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
	now := clock.New()
	mint := idgen.New()

	projects := application.NewProjects(uow, now, mint)
	tasks := application.NewTasks(uow, now, mint)

	app := httpapi.NewApp(httpapi.Deps{
		Projects:     projects,
		Tasks:        tasks,
		Comments:     application.NewComments(uow, now, mint),
		Artifacts:    application.NewArtifacts(uow, now, mint),
		Acceptance:   application.NewAcceptance(uow, now, mint),
		Relations:    application.NewRelations(uow, now, mint),
		Broker:       broker,
		ArtifactsDir: artifactsDir,
	})

	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, app)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Create a project and task
	actor := domain.Actor{Type: domain.ActorHuman, Name: "tester"}
	p, err := projects.Create(ctx, actor, "Test Project", "desc")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskRes, err := tasks.Create(ctx, actor, application.TaskInput{
		ProjectID: p.ID,
		Title:     "Test Task",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	return &testRig{
		server:       srv,
		artifactsDir: artifactsDir,
		db:           db,
		app:          app,
		projectID:    p.ID,
		taskID:       taskRes.ID,
	}
}

func createMultipartRequest(t *testing.T, url string, fieldName, filename string, content []byte, extraFields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for k, v := range extraFields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}

	part, err := writer.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestArtifactUpload_ImageSuccessAndServing(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	imgBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")

	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)
	req := createMultipartRequest(t, uploadURL, "file", "test-image.png", imgBytes, map[string]string{
		"actor_name": "agent-smith",
		"actor_type": "agent",
		"kind":       "screenshot",
		"summary":    "A verified screenshot",
	})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(res.Body)
		t.Fatalf("upload status = %d, want 201; body = %s", res.StatusCode, string(respBody))
	}

	var art struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Ref     string `json:"ref"`
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(res.Body).Decode(&art); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	if art.Kind != "screenshot" {
		t.Errorf("art.Kind = %q, want screenshot", art.Kind)
	}
	if art.Summary != "A verified screenshot" {
		t.Errorf("art.Summary = %q, want 'A verified screenshot'", art.Summary)
	}
	wantRef := "/artifacts/" + art.ID
	if art.Ref != wantRef {
		t.Errorf("art.Ref = %q, want %q", art.Ref, wantRef)
	}

	// Verify file on disk
	expectedFilename := art.ID + ".png"
	diskPath := filepath.Join(rig.artifactsDir, expectedFilename)
	data, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read stored file from disk: %v", err)
	}
	if !bytes.Equal(data, imgBytes) {
		t.Errorf("stored bytes do not match uploaded bytes")
	}

	// Verify GET /artifacts/{id}
	serveURL := rig.server.URL + wantRef
	serveRes, err := http.Get(serveURL)
	if err != nil {
		t.Fatalf("GET /artifacts/{id}: %v", err)
	}
	defer serveRes.Body.Close()

	if serveRes.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", serveRes.StatusCode)
	}
	if ct := serveRes.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if nosniff := serveRes.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if disp := serveRes.Header.Get("Content-Disposition"); disp != "inline" {
		t.Errorf("Content-Disposition = %q, want inline", disp)
	}

	servedBytes, err := io.ReadAll(serveRes.Body)
	if err != nil {
		t.Fatalf("read served body: %v", err)
	}
	if !bytes.Equal(servedBytes, imgBytes) {
		t.Errorf("served bytes do not match expected bytes")
	}

	// Also verify alias /api/v1/artifacts/{id}
	aliasURL := fmt.Sprintf("%s/api/v1/artifacts/%s", rig.server.URL, art.ID)
	aliasRes, err := http.Get(aliasURL)
	if err != nil {
		t.Fatalf("GET /api/v1/artifacts/{id}: %v", err)
	}
	defer aliasRes.Body.Close()
	if aliasRes.StatusCode != http.StatusOK {
		t.Errorf("alias GET status = %d, want 200", aliasRes.StatusCode)
	}
}

func TestArtifactUpload_NonImageAttachmentForceDownload(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	textContent := []byte("<html><script>alert('xss')</script></html>")

	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)
	req := createMultipartRequest(t, uploadURL, "file", "payload.html", textContent, map[string]string{
		"actor_name": "agent-smith",
		"actor_type": "agent",
		"kind":       "other",
		"summary":    "HTML payload",
	})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", res.StatusCode)
	}

	var art struct {
		ID  string `json:"id"`
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(res.Body).Decode(&art); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify GET /artifacts/{id} forces attachment download and nosniff
	serveRes, err := http.Get(rig.server.URL + art.Ref)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer serveRes.Body.Close()

	if serveRes.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", serveRes.StatusCode)
	}
	if nosniff := serveRes.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	disp := serveRes.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disp, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment", disp)
	}
}

func TestArtifactUpload_EmptyFileRejected(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)
	req := createMultipartRequest(t, uploadURL, "file", "empty.png", []byte{}, map[string]string{
		"actor_name": "agent-smith",
	})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("empty file status = %d, want 400", res.StatusCode)
	}
}

func TestArtifactUpload_OversizedFileRejected(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	// Max is 10MB; send 11MB
	oversized := make([]byte, 11<<20)

	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)
	req := createMultipartRequest(t, uploadURL, "file", "big.bin", oversized, map[string]string{
		"actor_name": "agent-smith",
	})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 400 || res.StatusCode >= 500 {
		t.Errorf("oversized file status = %d, want 4xx", res.StatusCode)
	}
}

func TestArtifactGet_PathTraversalAndNotFound(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	cases := []string{
		"../etc/passwd",
		"....//something",
		"01M3WA51KRSQ1AKYSE9MZEJJ8P", // non-existent valid ULID
		"invalid-id",
	}

	for _, id := range cases {
		url := fmt.Sprintf("%s/artifacts/%s", rig.server.URL, id)
		res, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 404 or 400", url, res.StatusCode)
		}
	}
}

func TestArtifactAttach_JSONBackCompat(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	payload := `{"actor":{"name":"tester","type":"human"},"kind":"url","ref":"https://example.com/build.log","summary":"Remote build log"}`
	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)

	res, err := http.Post(uploadURL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}

	var art struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(res.Body).Decode(&art); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if art.Ref != "https://example.com/build.log" {
		t.Errorf("ref = %q, want verbatim URL", art.Ref)
	}
}

func TestArtifactUpload_VideoAndAudioInlineServing(t *testing.T) {
	t.Parallel()
	rig := setupTestRig(t)

	videoBytes := []byte("fake-mp4-video-stream-bytes")
	uploadURL := fmt.Sprintf("%s/api/v1/tasks/%s/artifacts", rig.server.URL, rig.taskID)
	req := createMultipartRequest(t, uploadURL, "file", "screencast.mp4", videoBytes, map[string]string{
		"actor_name": "qa-agent",
		"actor_type": "agent",
		"kind":       "other",
		"summary":    "E2E run video",
	})

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", res.StatusCode)
	}

	var art struct {
		ID      string `json:"id"`
		Ref     string `json:"ref"`
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(res.Body).Decode(&art); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	if !strings.Contains(art.Summary, "screencast.mp4") {
		t.Errorf("art.Summary = %q, expected to preserve filename extension", art.Summary)
	}

	serveRes, err := http.Get(rig.server.URL + art.Ref)
	if err != nil {
		t.Fatalf("GET /artifacts/{id}: %v", err)
	}
	defer serveRes.Body.Close()

	if serveRes.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", serveRes.StatusCode)
	}
	if ct := serveRes.Header.Get("Content-Type"); !strings.HasPrefix(ct, "video/mp4") {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
	if disp := serveRes.Header.Get("Content-Disposition"); disp != "inline" {
		t.Errorf("Content-Disposition = %q, want inline for video", disp)
	}
}
