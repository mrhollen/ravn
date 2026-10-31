package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// clearExecEnv pins the three RAVN_* env vars runCLI resolves so each case
// starts from a known-empty environment (t.Setenv restores on cleanup).
func clearExecEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"RAVN_API_URL", "RAVN_TOKEN", "RAVN_AGENT_NAME"} {
		t.Setenv(key, "")
	}
}

// TestParseCLIArgs exercises flag parsing, validation and RAVN_* env resolution
// through the pure parse/resolve seam — no children spawned, no network touched.
func TestParseCLIArgs(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string // applied on top of a cleared environment
		args   []string
		code   int
		errSub string // substring expected in the stderr output when code != 0

		wantAPIURL     string
		wantToken      string
		wantActorName  string
		wantActorType  string
		wantTaskID     string
		wantTTL        int
		wantIntervalNs int64
		wantCommand    []string
	}{
		{
			name:   "unknown flag returns usage exit code",
			args:   []string{"--task-id", "T1", "--nope", "--", "true"},
			code:   2,
			errSub: "flag provided but not defined",
		},
		{
			name:   "invalid ttl value returns usage exit code",
			args:   []string{"--task-id", "T1", "--ttl-sec", "not-a-number", "--", "true"},
			code:   2,
			errSub: "invalid value",
		},
		{
			name:   "missing task-id prints usage line",
			args:   []string{"--", "true"},
			code:   2,
			errSub: "usage: ravn-exec --task-id <id>",
		},
		{
			name:   "no command after flags is rejected",
			args:   []string{"--task-id", "T1"},
			code:   2,
			errSub: "a command to run after the flags is required",
		},
		{
			name:          "env unset falls back to defaults",
			args:          []string{"--task-id", "T-DEFAULTS", "--", "ls", "-la"},
			wantAPIURL:    "http://ravn-server:8080",
			wantToken:     "",
			wantActorName: "agent",
			wantActorType: "agent",
			wantTaskID:    "T-DEFAULTS",
			wantTTL:       DefaultTTLSec,
			wantCommand:   []string{"ls", "-la"},
		},
		{
			name: "env vars override the defaults",
			env: map[string]string{
				"RAVN_API_URL":    "http://127.0.0.1:9999",
				"RAVN_TOKEN":      "sekret-token",
				"RAVN_AGENT_NAME": "worker-7",
			},
			args:          []string{"--task-id", "T-ENV", "--", "true"},
			wantAPIURL:    "http://127.0.0.1:9999",
			wantToken:     "sekret-token",
			wantActorName: "worker-7",
			wantActorType: "agent",
			wantTaskID:    "T-ENV",
			wantTTL:       DefaultTTLSec,
			wantCommand:   []string{"true"},
		},
		{
			name: "explicit flags win over env-driven defaults",
			env: map[string]string{
				"RAVN_AGENT_NAME": "worker-9",
			},
			args:           []string{"--task-id", "T-FLAGS", "--ttl-sec", "600", "--interval", "45s", "--", "sh", "-c", "exit 0"},
			wantAPIURL:     "http://ravn-server:8080",
			wantActorName:  "worker-9",
			wantActorType:  "agent",
			wantTaskID:     "T-FLAGS",
			wantTTL:        600,
			wantIntervalNs: int64(45e9),
			wantCommand:    []string{"sh", "-c", "exit 0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearExecEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			var errW strings.Builder
			res, code := parseCLIArgs(tc.args, &errW)
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, tc.code, errW.String())
			}
			if tc.code != 0 {
				if !strings.Contains(errW.String(), tc.errSub) {
					t.Errorf("stderr = %q, want it to contain %q", errW.String(), tc.errSub)
				}
				return
			}

			if res.apiURL != tc.wantAPIURL {
				t.Errorf("resolved apiURL = %q, want %q", res.apiURL, tc.wantAPIURL)
			}
			if res.token != tc.wantToken {
				t.Errorf("resolved token = %q, want %q", res.token, tc.wantToken)
			}
			if res.opts.Actor.Name != tc.wantActorName || res.opts.Actor.Type != tc.wantActorType {
				t.Errorf("resolved actor = %+v, want {%s %s}", res.opts.Actor, tc.wantActorType, tc.wantActorName)
			}
			if res.opts.TaskID != tc.wantTaskID {
				t.Errorf("resolved task id = %q, want %q", res.opts.TaskID, tc.wantTaskID)
			}
			if res.opts.TTLSec != tc.wantTTL {
				t.Errorf("resolved ttl = %d, want %d", res.opts.TTLSec, tc.wantTTL)
			}
			if got := int64(res.opts.HeartbeatInterval); got != tc.wantIntervalNs {
				t.Errorf("resolved interval = %v ns, want %d ns", got, tc.wantIntervalNs)
			}
			if strings.Join(res.opts.Command, " ") != strings.Join(tc.wantCommand, " ") {
				t.Errorf("resolved command = %v, want %v", res.opts.Command, tc.wantCommand)
			}
		})
	}
}

// leaseRecorder is a minimal fake of the ravn-server API surface ravn-exec uses:
// heartbeat / claim / release / comments. It records call order and request
// details (paths, bearer token, comment bodies) so the runCLI end-to-end test
// can assert the real apiclient wired against a resolved env URL drives the
// documented lease lifecycle.
type leaseRecorder struct {
	mu       sync.Mutex
	calls    []string
	auths    map[string]string // path -> Authorization header seen
	comments []string
	notHeld  bool // heartbeat answers 409 ("not claimed") until a claim lands
}

func (r *leaseRecorder) handler() http.Handler {
	if r.auths == nil {
		r.auths = make(map[string]string)
	}
	mux := http.NewServeMux()
	task := "/api/v1/tasks/T-CLI"
	summary := `{"id":"T-CLI","title":"wiring","status":"doing","priority":0,"version":2,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	record := func(op string, auth string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, op)
		if auth != "" {
			r.auths[task+"/"+op] = auth
		}
	}

	mux.HandleFunc("POST "+task+"/heartbeat", func(w http.ResponseWriter, req *http.Request) {
		record("heartbeat", req.Header.Get("Authorization"))
		r.mu.Lock()
		held := !r.notHeld || len(r.calls) > 1 // first heartbeat misses; later ones hold
		r.mu.Unlock()
		if held {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(summary)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"claim_conflict","message":"not claimed: task has no active lease"}`)) //nolint:errcheck
	})
	mux.HandleFunc("POST "+task+"/claim", func(w http.ResponseWriter, req *http.Request) {
		record("claim", req.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(summary)) //nolint:errcheck
	})
	mux.HandleFunc("POST "+task+"/release", func(w http.ResponseWriter, req *http.Request) {
		record("release", req.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"T-CLI","title":"wiring","status":"todo","priority":0,"version":3,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)) //nolint:errcheck
	})
	mux.HandleFunc("POST "+task+"/comments", func(w http.ResponseWriter, req *http.Request) {
		record("comment", req.Header.Get("Authorization"))
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.comments = append(r.comments, body.Body)
		r.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"c1","kind":"progress","body":"ok","actor":{"type":"agent","name":"agent"},"created_at":"2026-01-01T00:00:00Z"}`)) //nolint:errcheck
	})
	return mux
}

func (r *leaseRecorder) snapshot() (calls, comments []string, auths map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := make([]string, len(r.calls))
	copy(c, r.calls)
	cm := make([]string, len(r.comments))
	copy(cm, r.comments)
	a := make(map[string]string, len(r.auths))
	for k, v := range r.auths {
		a[k] = v
	}
	return c, cm, a
}

// TestRunCLIEndToEndAgainstFakeAPI drives the REAL runCLI (flag parsing → env
// resolution → apiclient.New → Run → runForeground) against an in-process fake
// API. It proves the foreground spawn path end-to-end: a clean child exit ends 0
// with no failure comment, and a failing child preserves the child's exit code
// AND posts the progress comment naming it before the release.
func TestRunCLIEndToEndAgainstFakeAPI(t *testing.T) {
	cases := []struct {
		name        string
		command     []string
		wantCode    int
		wantCalls   []string
		wantComment string // substring expected in the single failure comment; "" = none
	}{
		{
			name:      "clean child exit runs claim-then-release without a comment",
			command:   []string{"true"},
			wantCode:  0,
			wantCalls: []string{"heartbeat", "claim", "release"},
		},
		{
			name:        "failing child preserves its code and posts a progress comment first",
			command:     []string{"sh", "-c", "exit 5"},
			wantCode:    5,
			wantCalls:   []string{"heartbeat", "claim", "comment", "release"},
			wantComment: "code 5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &leaseRecorder{notHeld: true}
			api := httptest.NewServer(rec.handler())
			t.Cleanup(api.Close)

			clearExecEnv(t)
			t.Setenv("RAVN_API_URL", api.URL)
			t.Setenv("RAVN_TOKEN", "tok-cli")
			t.Setenv("RAVN_AGENT_NAME", "e2e-agent")

			args := append([]string{"--task-id", "T-CLI"}, tc.command...)
			var errW strings.Builder
			code := runCLI(args, &errW)
			if code != tc.wantCode {
				t.Fatalf("runCLI exit = %d, want %d (stderr: %s)", code, tc.wantCode, errW.String())
			}

			calls, comments, auths := rec.snapshot()
			if strings.Join(calls, " ") != strings.Join(tc.wantCalls, " ") {
				t.Errorf("API call order = %v, want %v", calls, tc.wantCalls)
			}
			if got := auths["/api/v1/tasks/T-CLI/claim"]; got != "Bearer tok-cli" {
				t.Errorf("claim Authorization = %q, want %q (RAVN_TOKEN must reach the real client)", got, "Bearer tok-cli")
			}
			if tc.wantComment == "" {
				if len(comments) != 0 {
					t.Errorf("clean exit posted %d failure comment(s), want none: %v", len(comments), comments)
				}
			} else {
				if len(comments) != 1 {
					t.Fatalf("failure run posted %d comment(s), want exactly 1: %v", len(comments), comments)
				}
				if !strings.Contains(comments[0], tc.wantComment) || !strings.Contains(comments[0], strings.Join(tc.command, " ")) {
					t.Errorf("failure comment = %q, want it to name the command and contain %q", comments[0], tc.wantComment)
				}
			}
		})
	}
}

// TestRunForeground pins the exit-code contract of the foreground spawn seam:
// a successful child ends 0; a non-zero child exit is propagated verbatim; a
// command that cannot even start reports 127 (the shell "not found" convention).
func TestRunForeground(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		want    int
	}{
		{"successful child exits zero", []string{"true"}, 0},
		{"child exit code is propagated", []string{"sh", "-c", "exit 3"}, 3},
		{"missing binary reports 127", []string{"/nonexistent/ravn-exec-missing-binary-xyz"}, 127},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runForeground(tc.command)(); got != tc.want {
				t.Errorf("runForeground(%v)() = %d, want %d", tc.command, got, tc.want)
			}
		})
	}
}
