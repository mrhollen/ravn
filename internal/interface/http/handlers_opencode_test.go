package httpapi

// Tests for the OpenCode dispatch prompt (Epic 01M5XSNG2ZK5VSS7QTME49Z1WM AC3):
// formatOpenCodePrompt must carry full resume context — durable notes, open
// blockers, and recent comments — each omitted when empty, plus a
// handleOpenCodeStart integration check against the real task detail.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// openCodeDetailFixture builds a TaskDetail for formatOpenCodePrompt unit
// tests. comments must be passed NEWEST FIRST (as GetDetail returns them).
func openCodeDetailFixture(notes string, blockers []domain.Task, comments []domain.Comment) *application.TaskDetail {
	return &application.TaskDetail{
		Task: domain.Task{
			ID:          "TASK-OC",
			Title:       "Resume Context Task",
			Description: "work needs context to resume",
			Status:      domain.StatusDoing,
			Notes:       notes,
		},
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Seq: 1, Text: "AC one"}},
		Blockers:           blockers,
		Comments:           comments,
	}
}

func TestFormatOpenCodePrompt_NotesSection(t *testing.T) {
	t.Run("non-empty notes are included verbatim", func(t *testing.T) {
		const note = "## Findings\n- root cause in retry loop (pkg/foo)\nNext: patch backoff."
		prompt := formatOpenCodePrompt(openCodeDetailFixture(note, nil, nil))
		if !strings.Contains(prompt, "Resume Notes (prior context") {
			t.Errorf("prompt missing notes section heading:\n%s", prompt)
		}
		if !strings.Contains(prompt, note) {
			t.Errorf("prompt missing verbatim notes content:\n%s", prompt)
		}
	})

	t.Run("empty notes omit the section entirely", func(t *testing.T) {
		for _, empty := range []string{"", "   \n"} {
			prompt := formatOpenCodePrompt(openCodeDetailFixture(empty, nil, nil))
			if strings.Contains(prompt, "Resume Notes") {
				t.Errorf("prompt must omit notes section when notes are %q:\n%s", empty, prompt)
			}
		}
	})
}

func TestFormatOpenCodePrompt_BlockersSection(t *testing.T) {
	open := []domain.Task{
		{ID: "BLK-A", Title: "Fix auth migration", Status: domain.StatusDoing},
		{ID: "BLK-B", Title: "Ship feature flag", Status: domain.StatusTodo},
	}
	t.Run("open blockers are listed with id, title and status", func(t *testing.T) {
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", open, nil))
		for _, want := range []string{
			"Open Blockers",
			"[BLK-A] Fix auth migration (status: doing)",
			"[BLK-B] Ship feature flag (status: todo)",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
	})

	t.Run("no open blockers omit the section entirely", func(t *testing.T) {
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, nil))
		if strings.Contains(prompt, "Open Blockers") {
			t.Errorf("prompt must omit blockers section when there are none:\n%s", prompt)
		}
	})

	t.Run("done blocker targets never reach the formatter (pre-filtered by GetDetail)", func(t *testing.T) {
		// TaskDetail.Blockers is computed at read time as blocked-by edges whose
		// targets are NOT done/archived; a done target therefore simply is not in
		// the slice, and the prompt must not name it.
		doneOnly := []domain.Task{} // what GetDetail would return for done-only targets
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", doneOnly, nil))
		if strings.Contains(prompt, "Open Blockers") {
			t.Errorf("prompt must omit blockers section when all blocked-by targets are done:\n%s", prompt)
		}
	})
}

func TestFormatOpenCodePrompt_CommentsSection(t *testing.T) {
	mkComments := func(n int) []domain.Comment {
		cs := make([]domain.Comment, 0, n)
		now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
		for i := 0; i < n; i++ { // newest first
			cs = append(cs, domain.Comment{
				ID:        domain.ID(fmt.Sprintf("CM-%d", n-i)),
				TaskID:    "TASK-OC",
				Kind:      domain.CommentProgress,
				Body:      fmt.Sprintf("comment-body-%d", n-i), // comment-body-n is NEWEST
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "agent-x"},
				CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			})
		}
		return cs
	}

	t.Run("up to 5 comments rendered newest first with kind labels", func(t *testing.T) {
		cs := mkComments(3)
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, cs))
		for _, want := range []string{"Recent Comments", "**agent-x** (progress): comment-body-3"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
		// Newest first: body-3 (newest) must appear before body-1 (oldest).
		if iNewest, iOldest := strings.Index(prompt, "comment-body-3"), strings.Index(prompt, "comment-body-1"); iNewest == -1 || iOldest == -1 || iNewest > iOldest {
			t.Errorf("comments not newest first:\n%s", prompt)
		}
	})

	t.Run("more than 5 comments are capped at the 5 newest", func(t *testing.T) {
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, mkComments(7)))
		for _, want := range []string{"comment-body-7", "comment-body-6", "comment-body-5", "comment-body-4", "comment-body-3"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing expected newest comment %q:\n%s", want, prompt)
			}
		}
		for _, notWant := range []string{"comment-body-2", "comment-body-1"} {
			if strings.Contains(prompt, notWant) {
				t.Errorf("prompt must cap at 5 comments; leaked %q:\n%s", notWant, prompt)
			}
		}
	})

	t.Run("handoff and question kinds stay visible via their kind label", func(t *testing.T) {
		cs := []domain.Comment{
			{ID: "CM-H", TaskID: "TASK-OC", Kind: domain.CommentHandoff, Body: "stepping away — see notes", Actor: domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}, CreatedAt: time.Now()},
			{ID: "CM-Q", TaskID: "TASK-OC", Kind: domain.CommentQuestion, Body: "should we use SQLite here?", Actor: domain.Actor{Type: domain.ActorHuman, Name: "human-lead"}, CreatedAt: time.Now()},
		}
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, cs))
		for _, want := range []string{"**agent-a** (handoff): stepping away — see notes", "**human-lead** (question): should we use SQLite here?"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
	})

	t.Run("no comments omit the section entirely", func(t *testing.T) {
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, nil))
		if strings.Contains(prompt, "Recent Comments") {
			t.Errorf("prompt must omit comments section when there are none:\n%s", prompt)
		}
	})

	t.Run("base sections and instructions remain intact", func(t *testing.T) {
		prompt := formatOpenCodePrompt(openCodeDetailFixture("", nil, nil))
		for _, want := range []string{
			"Task ID: TASK-OC",
			"Title: Resume Context Task",
			"Description: work needs context to resume",
			"- [ ] Seq 1: AC one",
			"Instructions:",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing base content %q:\n%s", want, prompt)
			}
		}
		if !strings.HasSuffix(strings.TrimSpace(prompt), "commit and push your code changes to the git repository.") {
			t.Errorf("instructions block no longer ends the prompt:\n%s", prompt)
		}
	})
}

// TestOpenCodeStart_PromptIncludesResumeContext is the integration-style AC3
// check: handleOpenCodeStart formats the dispatch prompt from the REAL loaded
// TaskDetail, so notes, open blockers (a done blocked-by target must NOT be
// listed) and recent comments flow into the dispatched text.
func TestOpenCodeStart_PromptIncludesResumeContext(t *testing.T) {
	var receivedPromptText string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-resume-ctx"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/prompt_async"):
			var req struct {
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(req.Parts) > 0 && req.Parts[0].Type == "text" {
				receivedPromptText = req.Parts[0].Text
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	stk := newStack(t)
	stk.App.HTTPClient = mockServer.Client()

	p := createProject(t, stk, "Resume Context Project")
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
		"opencode_directory":  "/home/test/resume-ctx",
	})

	// Blockers: A is open (todo), D is done (must NOT be listed as open).
	blockerA := makeTask(t, stk, p.ID, "Finish prerequisite migration", nil)
	blockerD := makeTask(t, stk, p.ID, "Old finished blocker", nil)
	patchTask(t, stk, blockerD.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, blockerD.ID, 2, map[string]any{"status": "done"})

	target := makeTask(t, stk, p.ID, "Resume context task", nil)
	for _, b := range []taskSummaryDTO{blockerA, blockerD} {
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+target.ID+"/relations", map[string]any{
			"actor": actor("eve"), "other_id": b.ID, "kind": "blocked-by",
		})
		if code != http.StatusCreated {
			t.Fatalf("POST relations (blocked-by %s) = %d, want 201 (body %s)", b.ID, code, string(body))
		}
	}

	const note = "## Findings\n- root cause in retry loop (pkg/foo line 42)\nNext step: patch the backoff schedule."
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/tasks/"+target.ID+"/notes", map[string]any{
		"actor": actor("agent-prev"), "notes": note,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT notes = %d (body %s)", code, string(body))
	}

	// Comments oldest→newest; newest-first tail in the prompt must be:
	// handoff > question > progress.
	addComment := func(kind, name, actorType, cBody string) {
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+target.ID+"/comments", map[string]any{
			"actor": map[string]any{"type": actorType, "name": name}, "kind": kind, "body": cBody,
		})
		if code != http.StatusCreated {
			t.Fatalf("POST comments (%s) = %d (body %s)", kind, code, string(body))
		}
	}
	addComment("progress", "agent-prev", "agent", "got the retry loop fixed locally")
	addComment("question", "human-lead", "human", "should this also backfill old rows?")
	addComment("handoff", "agent-prev", "agent", "stepping away — next agent: verify against prod schema first")

	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+target.ID+"/opencode/start", nil)
	if code != http.StatusOK {
		t.Fatalf("POST opencode/start = %d (body %s)", code, string(body))
	}
	if receivedPromptText == "" {
		t.Fatal("dispatched prompt text was empty")
	}

	// Base + notes
	for _, want := range []string{
		"Task ID: " + target.ID,
		"Title: Resume context task",
		"Resume Notes (prior context",
		note,
	} {
		if !strings.Contains(receivedPromptText, want) {
			t.Errorf("dispatched prompt missing %q:\n%s", want, receivedPromptText)
		}
	}

	// Open blocker listed; DONE blocked-by target NOT listed.
	if !strings.Contains(receivedPromptText, "Open Blockers") ||
		!strings.Contains(receivedPromptText, "["+blockerA.ID+"] Finish prerequisite migration (status: todo)") {
		t.Errorf("dispatched prompt missing open blocker entry:\n%s", receivedPromptText)
	}
	if strings.Contains(receivedPromptText, "Old finished blocker") || strings.Contains(receivedPromptText, blockerrIDMarker(blockerD.ID)) {
		t.Errorf("dispatched prompt must NOT list the done blocked-by target as an open blocker:\n%s", receivedPromptText)
	}

	// Comments: newest first with kind labels, handoff before question before progress.
	iHandoff := strings.Index(receivedPromptText, "**agent-prev** (handoff): stepping away — next agent")
	iQuestion := strings.Index(receivedPromptText, "**human-lead** (question): should this also backfill old rows?")
	iProgress := strings.Index(receivedPromptText, "**agent-prev** (progress): got the retry loop fixed locally")
	if iHandoff == -1 || iQuestion == -1 || iProgress == -1 {
		t.Errorf("dispatched prompt missing labeled comments:\n%s", receivedPromptText)
	} else if !(iHandoff < iQuestion && iQuestion < iProgress) {
		t.Errorf("dispatched comments not newest first (handoff=%d question=%d progress=%d):\n%s", iHandoff, iQuestion, iProgress, receivedPromptText)
	}

	// Instructions block intact.
	if !strings.Contains(receivedPromptText, "Instructions: You are assigned to autonomously work on and complete this task in Rävn.") {
		t.Errorf("dispatched prompt missing instructions block:\n%s", receivedPromptText)
	}
}

// blockerrIDMarker quotes the blocker ID exactly as the Open Blockers section
// renders it, so a done target cannot sneak into the listing under any wording.
func blockerrIDMarker(id string) string { return "[" + id + "]" }
