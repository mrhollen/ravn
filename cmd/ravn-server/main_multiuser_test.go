package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file covers the multi-user epic (T3/T4) startup-validation seams in
// run(): every RAVN_AUTH / RAVN_SESSION_TTL / RAVN_IDENTITY_OPEN /
// RAVN_SCOPING misconfiguration must fail fast with exit 1 BEFORE any socket
// binds or database opens, and the valid multiuser + membership combination
// must boot into exactly the documented posture (anonymous → 401 on API
// routes, shared token still works as operator, /auth/login reachable).

// runExitsFast launches run() in a goroutine and waits for it to return on its
// own. It is only safe while run() cannot reach ListenAndServe — every case
// here returns during env validation, before the signal handler is installed —
// so no SIGTERM is ever sent and none can leak into the test process.
func runExitsFast(t *testing.T, buf *lockedBuffer, args ...string) <-chan int {
	t.Helper()
	codeCh := make(chan int, 1)
	go func() { codeCh <- run(args, buf) }()
	return codeCh
}

// TestServerRunMultiuserEnvValidation is the fail-fast matrix: each bad env
// value exits 1 with its dedicated log line, before DB/socket side effects.
func TestServerRunMultiuserEnvValidation(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantLog string
	}{
		{
			name:    "bogus RAVN_AUTH fails fast",
			env:     map[string]string{"RAVN_AUTH": "multi-user-typo"},
			wantLog: "invalid RAVN_AUTH",
		},
		{
			name:    "RAVN_AUTH=token without a token fails fast",
			env:     map[string]string{"RAVN_AUTH": "token"},
			wantLog: "RAVN_AUTH=token requires a non-empty token",
		},
		{
			name:    "unparseable RAVN_SESSION_TTL fails fast",
			env:     map[string]string{"RAVN_SESSION_TTL": "banana"},
			wantLog: "invalid RAVN_SESSION_TTL",
		},
		{
			name:    "non-positive RAVN_SESSION_TTL fails fast",
			env:     map[string]string{"RAVN_SESSION_TTL": "-1h"},
			wantLog: "invalid RAVN_SESSION_TTL",
		},
		{
			name:    "bogus RAVN_IDENTITY_OPEN fails fast",
			env:     map[string]string{"RAVN_IDENTITY_OPEN": "maybe"},
			wantLog: "invalid RAVN_IDENTITY_OPEN",
		},
		{
			name:    "bogus RAVN_SCOPING fails fast",
			env:     map[string]string{"RAVN_SCOPING": "per-project"},
			wantLog: "invalid RAVN_SCOPING",
		},
		{
			// Membership scoping with auth off (no token, no RAVN_AUTH) would
			// fail-closed lock out every request — refuse to boot.
			name:    "membership scoping without auth fails fast",
			env:     map[string]string{"RAVN_SCOPING": "membership"},
			wantLog: "requires RAVN_AUTH=token or multiuser",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearServerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			addr := freeAddr(t)

			buf := &lockedBuffer{}
			codeCh := runExitsFast(t, buf, bootArgs(t, addr)...)
			if got := waitRunExit(t, codeCh, buf); got != 1 {
				t.Fatalf("run() exit = %d, want 1; logs:\n%s", got, buf.String())
			}
			if !strings.Contains(buf.String(), tc.wantLog) {
				t.Errorf("logs do not contain %q; logs:\n%s", tc.wantLog, buf.String())
			}
			// Fail-fast means the socket was never bound: a dial must fail.
			open := portOpen(addr)
			if open {
				t.Errorf("server is listening on %s although validation should have exited first", addr)
			}
		})
	}
}

// TestServerRunMultiuserWarningForTokenMode proves the advisory case: membership
// scoping + RAVN_AUTH=token (explicit, or implicit via RAVN_TOKEN) boots — but
// logs the warning that shared-token requests all act as the bypassing operator.
func TestServerRunMultiuserWarningForTokenMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "explicit token mode", env: map[string]string{"RAVN_AUTH": "token", "RAVN_TOKEN": "s3cr3t", "RAVN_SCOPING": "membership"}},
		{name: "implicit token mode via RAVN_TOKEN", env: map[string]string{"RAVN_TOKEN": "s3cr3t", "RAVN_SCOPING": "membership"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearServerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			addr := freeAddr(t)

			codeCh, buf := startRunServer(t, addr, bootArgs(t, addr)...)
			defer func() { stopRunServer(t, codeCh, buf) }()

			if !strings.Contains(buf.String(), "RAVN_SCOPING=membership with RAVN_AUTH=token") {
				t.Errorf("logs missing the token-mode membership warning; logs:\n%s", buf.String())
			}
		})
	}
}

// TestServerRunMultiuserBootPosture proves what a valid multiuser deployment
// actually does at runtime: anonymous API calls are 401, the shared RAVN_TOKEN
// still acts as privileged operator (and gates /metrics), POST /auth/login is
// public and answers 401 for unknown credentials on an empty identity store,
// and a credentialless session endpoint is 401.
func TestServerRunMultiuserBootPosture(t *testing.T) {
	t.Run("multiuser with shared token set", func(t *testing.T) {
		clearServerEnv(t)
		const operatorToken = "operator-secret"
		t.Setenv("RAVN_AUTH", "multiuser")
		t.Setenv("RAVN_TOKEN", operatorToken)
		t.Setenv("RAVN_SCOPING", "membership")

		addr := freeAddr(t)
		codeCh, _ := startRunServer(t, addr, bootArgs(t, addr)...)
		defer func() { stopRunServer(t, codeCh, nil) }()
		base := "http://" + addr

		if code, body := get(t, base+"/api/v1/projects"); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET /api/v1/projects = %d (body %s), want 401", code, body)
		}
		if code, _ := getAuthed(t, base+"/api/v1/projects", "Bearer "+operatorToken); code != http.StatusOK {
			t.Errorf("operator GET /api/v1/projects = %d, want 200", code)
		}
		if code, _ := getAuthed(t, base+"/metrics", "Bearer "+operatorToken); code != http.StatusOK {
			t.Errorf("operator GET /metrics = %d, want 200", code)
		}
		if code, body := get(t, base+"/metrics"); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET /metrics = %d (body %s), want 401 (token set)", code, body)
		}

		login := `{"username":"ghost","password":"nope"}`
		code, body := postJSON(t, base+"/api/v1/auth/login", login, "")
		if code != http.StatusUnauthorized {
			t.Errorf("POST /auth/login (unknown user) = %d (body %s), want 401", code, body)
		}
		if !strings.Contains(body, "invalid username or password") {
			t.Errorf("POST /auth/login error message = %q, want the unenumerable 401 wording", body)
		}

		// The route is live (not 404), but the shared-operator has NO session row:
		// InspectSession answers ErrAuthNoSession → 401 "no active session".
		if code, body := getAuthed(t, base+"/api/v1/auth/session", "Bearer "+operatorToken); code != http.StatusUnauthorized || !strings.Contains(body, "no active session") {
			t.Errorf("operator GET /auth/session = %d (body %s), want 401 no-active-session", code, body)
		}
		if code, _ := get(t, base+"/api/v1/auth/session"); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET /auth/session = %d, want 401 (gate rejects missing bearer before the handler)", code)
		}

		// Membership management is admin-or-owner gated; with zero members an
		// operator (bypassing principal) may list the roster.
		pid := createProjectAs(t, base, operatorToken)
		membersURL := base + "/api/v1/projects/" + pid + "/members"
		if code, body := getAuthed(t, membersURL, "Bearer "+operatorToken); code != http.StatusOK {
			t.Errorf("operator GET /projects/{id}/members = %d (body %s), want 200", code, body)
		}
	})

	t.Run("full login round-trip as a provisioned human", func(t *testing.T) {
		clearServerEnv(t)
		const operatorToken = "operator-secret"
		t.Setenv("RAVN_AUTH", "multiuser")
		t.Setenv("RAVN_TOKEN", operatorToken)
		t.Setenv("RAVN_SCOPING", "membership")

		addr := freeAddr(t)
		codeCh, _ := startRunServer(t, addr, bootArgs(t, addr)...)
		defer func() { stopRunServer(t, codeCh, nil) }()
		base := "http://" + addr
		opBearer := "Bearer " + operatorToken

		// 1) Operator provisions a human account (201 on create).
		code, body := postJSON(t, base+"/api/v1/auth/accounts",
			`{"name":"alice","kind":"human","password":"correct-horse-battery"}`, opBearer)
		if code != http.StatusCreated {
			t.Fatalf("operator POST /auth/accounts = %d (body %s), want 201", code, body)
		}

		// 2) Login exchanges username+password for a session token.
		code, body = postJSON(t, base+"/api/v1/auth/login",
			`{"username":"alice","password":"correct-horse-battery"}`, "")
		if code != http.StatusOK {
			t.Fatalf("POST /auth/login (valid) = %d (body %s), want 200", code, body)
		}
		var lr struct {
			Token    string `json:"token"`
			Identity struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"identity"`
		}
		if err := json.Unmarshal([]byte(body), &lr); err != nil || lr.Token == "" {
			t.Fatalf("cannot parse session token from %q: %v", body, err)
		}
		if lr.Identity.Name != "alice" || lr.Identity.Kind != "human" {
			t.Errorf("login identity = %+v, want name=alice kind=human", lr.Identity)
		}

		// 3) The session token authenticates the credential principal.
		code, body = getAuthed(t, base+"/api/v1/auth/session", "Bearer "+lr.Token)
		if code != http.StatusOK || !strings.Contains(body, `"alice"`) {
			t.Errorf("session GET /auth/session = %d (body %s), want 200 with alice", code, body)
		}
		if code, _ := getAuthed(t, base+"/api/v1/projects", "Bearer "+lr.Token); code != http.StatusOK {
			t.Errorf("session GET /api/v1/projects = %d, want 200", code)
		}

		// 4) The credential principal is NOT an operator: provisioning others'
		//    accounts is refused with 403 even though the request authenticated.
		code, _ = postJSON(t, base+"/api/v1/auth/accounts",
			`{"name":"mallory","kind":"human","password":"also-correct-horse"}`, "Bearer "+lr.Token)
		if code != http.StatusForbidden {
			t.Errorf("credential POST /auth/accounts = %d, want 403 operator-only", code)
		}

		// 5) Wrong password is the SAME unenumerable 401 as unknown username.
		code, _ = postJSON(t, base+"/api/v1/auth/login",
			`{"username":"alice","password":"wrong"}`, "")
		if code != http.StatusUnauthorized {
			t.Errorf("POST /auth/login (bad password) = %d, want 401", code)
		}
	})

	t.Run("multiuser without shared token", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("RAVN_AUTH", "multiuser")
		t.Setenv("RAVN_SCOPING", "membership")

		addr := freeAddr(t)
		codeCh, _ := startRunServer(t, addr, bootArgs(t, addr)...)
		defer func() { stopRunServer(t, codeCh, nil) }()
		base := "http://" + addr

		if code, _ := get(t, base+"/api/v1/projects"); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET /api/v1/projects = %d, want 401", code)
		}
		// No shared token configured → root /metrics is NOT gated (token-gate
		// only applies when a token exists), even in multiuser mode.
		if code, _ := get(t, base+"/metrics"); code != http.StatusOK {
			t.Errorf("GET /metrics with no RAVN_TOKEN = %d, want 200 (ungated)", code)
		}
	})
}

// ---- local helpers (kept here so this file stays self-contained) ----------

// freeAddr reserves a free loopback port and returns "127.0.0.1:<port>" —
// reuses the package-level freePort helper from main_wiring_test.go.
func freeAddr(t *testing.T) string {
	t.Helper()
	return "127.0.0.1:" + strconv.Itoa(freePort(t))
}

// portOpen reports whether something is currently listening on addr (used to
// prove the fail-fast cases exited BEFORE binding).
func portOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func postJSON(t *testing.T, url, body, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read POST %s body: %v", url, err)
	}
	return res.StatusCode, buf.String()
}

func getAuthed(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read GET %s body: %v", url, err)
	}
	return res.StatusCode, buf.String()
}

// createProjectAs POSTs a project as the operator (bypassing principal) and
// returns its id — used to build a members URL for the roster assertion.
func createProjectAs(t *testing.T, base, bearer string) string {
	t.Helper()
	// createProjectReq uses "name" (not "title") and REQUIRES an actor.name.
	body := `{"name":"e2e-multiuser-posture","actor":{"type":"human","name":"operator"}}`
	code, respBody := postJSON(t, base+"/api/v1/projects", body, "Bearer "+bearer)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("operator POST /projects = %d (body %s), want 201/200", code, respBody)
	}
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(respBody), &row); err != nil || row.ID == "" {
		t.Fatalf("cannot parse project id from %q: %v", respBody, err)
	}
	return row.ID
}
