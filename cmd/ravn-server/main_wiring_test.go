package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// lockedBuffer is a mutex-guarded io.Writer with a String() accessor. run()'s
// slog handler writes into it from the server goroutines while test goroutines
// may read captured logs (and vice versa during the shutdown window), so every
// write and read must share one lock — a plain bytes.Buffer would be a data race.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// clearServerEnv pins every env var run() resolves so each case starts from a
// known-empty environment (t.Setenv restores on cleanup). Tests that want an
// override set the specific key afterwards.
func clearServerEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"RAVN_ADDR", "RAVN_DATA", "RAVN_TOKEN", "RAVN_STATIC_DIR",
		"RAVN_ARTIFACTS_DIR", "LOG_LEVEL", "RAVN_LOG_LEVEL", "RAVN_CORS_ORIGINS",
	} {
		t.Setenv(key, "")
	}
}

// freePort reserves a free TCP port on loopback for a server under test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// makeDist builds a Vite-shaped dist directory; withIndex controls whether the
// index.html marker (a built web UI) is present.
func makeDist(t *testing.T, withIndex bool) string {
	t.Helper()
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.js"), []byte(appJS), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	if withIndex {
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexHTML), 0o644); err != nil {
			t.Fatalf("write index.html: %v", err)
		}
	}
	return dir
}

// startRunServer boots run() in a goroutine with the given argv and waits until
// /healthz on probeAddr answers 200 (the server is fully up: flags resolved, DB
// open+migrated, mux wired, ListenAndServe bound).
func startRunServer(t *testing.T, probeAddr string, args ...string) (<-chan int, *lockedBuffer) {
	t.Helper()
	buf := &lockedBuffer{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- run(args, buf) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get("http://" + probeAddr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return codeCh, buf
			}
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatalf("server on %s did not answer /healthz within 5s; logs:\n%s", probeAddr, buf.String())
	return nil, nil
}

// stopRunServer sends SIGTERM to this process and waits for run() to return its
// exit code. The signal is only ever sent AFTER startRunServer confirmed the
// server healthy — by then run()'s signal.NotifyContext handler is guaranteed
// registered (it is installed before ListenAndServe starts) — so the SIGTERM
// can never leak and kill the test process, even with t.Parallel tests running.
func stopRunServer(t *testing.T, codeCh <-chan int, buf *lockedBuffer) int {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to self: %v", err)
	}
	select {
	case code := <-codeCh:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("run() did not exit within 10s of SIGTERM; logs:\n%s", buf.String())
		return 0 // unreachable
	}
}

// waitRunExit waits for a run() goroutine that returns on its OWN (no signal
// needed): bind conflicts, DB-open failures, artifacts-dir failures and flag
// parse errors all return before or without ever waiting on a signal.
func waitRunExit(t *testing.T, codeCh <-chan int, buf *lockedBuffer) int {
	t.Helper()
	select {
	case code := <-codeCh:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("run() did not return within 10s; logs:\n%s", buf.String())
		return 0 // unreachable
	}
}

// bootArgs returns the argv shared by every full-boot test: a fresh temp DB and
// artifacts dir so no test ever writes into the repo (and RAVN_STATIC_DIR's
// default of ./web/dist is never consulted). An empty addr OMITS the --addr
// flag entirely (passing "--addr \"\"" would override the env-driven default —
// exactly what we must avoid in the env-precedence case).
func bootArgs(t *testing.T, addr string, extra ...string) []string {
	t.Helper()
	var args []string
	if addr != "" {
		args = append(args, "--addr", addr)
	}
	args = append(args, "--data", filepath.Join(t.TempDir(), "ravn.db"), "--artifacts-dir", t.TempDir())
	return append(args, extra...)
}

// TestServerRunStaticDirPrecedence proves the static-dir resolution chain in the
// real entry point: an explicit --static flag beats $RAVN_STATIC_DIR; the env var
// is used when the flag is omitted; and a dist without index.html degrades to
// the placeholder root (asserted both on GET / and via the router's log line).
func TestServerRunStaticDirPrecedence(t *testing.T) {
	distWithIndex := makeDist(t, true)
	distNoIndex := makeDist(t, false)

	cases := []struct {
		name         string
		envStatic    string // "" = RAVN_STATIC_DIR left unset (cleared)
		flagStatic   string // "" = --static flag omitted
		wantRootBody string
		wantLogLine  string
	}{
		{
			name:         "explicit static flag beats env var",
			envStatic:    distNoIndex,
			flagStatic:   distWithIndex,
			wantRootBody: "Rävn UI",
			wantLogLine:  "serving built web UI",
		},
		{
			name:         "env var used when flag omitted",
			envStatic:    distWithIndex,
			flagStatic:   "",
			wantRootBody: "Rävn UI",
			wantLogLine:  "serving built web UI",
		},
		{
			name:         "no index anywhere keeps the placeholder root",
			envStatic:    distNoIndex,
			flagStatic:   "",
			wantRootBody: "ravn server ok",
			wantLogLine:  "no index.html in static dir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearServerEnv(t)
			if tc.envStatic != "" {
				t.Setenv("RAVN_STATIC_DIR", tc.envStatic)
			}

			addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
			args := bootArgs(t, addr)
			if tc.flagStatic != "" {
				args = append(args, "--static", tc.flagStatic)
			}
			codeCh, buf := startRunServer(t, addr, args...)

			code, body := get(t, "http://"+addr+"/")
			if code != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200 (body %s)", code, body)
			}
			if !strings.Contains(body, tc.wantRootBody) {
				t.Errorf("GET / body = %q, want it to contain %q", body, tc.wantRootBody)
			}
			if !strings.Contains(buf.String(), tc.wantLogLine) {
				t.Errorf("server logs do not contain %q; logs:\n%s", tc.wantLogLine, buf.String())
			}

			if got := stopRunServer(t, codeCh, buf); got != 0 {
				t.Fatalf("run() exit = %d after SIGTERM, want 0 (clean graceful shutdown)", got)
			}
			if !strings.Contains(buf.String(), "shutdown signal received") || !strings.Contains(buf.String(), "stopped") {
				t.Errorf("shutdown logs missing; logs:\n%s", buf.String())
			}
		})
	}
}

// TestServerRunAddrAndTokenPrecedence proves the RAVN_ADDR / RAVN_TOKEN env
// resolution: the env var is honored when its flag is omitted, an explicit flag
// beats the env var (the env's port is never bound), and a token from the
// environment gates /api/v1 exactly like one passed via --token.
func TestServerRunAddrAndTokenPrecedence(t *testing.T) {
	t.Run("env addr used when flag omitted", func(t *testing.T) {
		clearServerEnv(t)
		addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
		t.Setenv("RAVN_ADDR", addr)

		codeCh, _ := startRunServer(t, addr, bootArgs(t, "")...) // no --addr flag at all
		if got := stopRunServer(t, codeCh, nil); got != 0 {
			t.Fatalf("run() exit = %d after SIGTERM, want 0", got)
		}
	})

	t.Run("flag addr beats env addr", func(t *testing.T) {
		clearServerEnv(t)
		envAddr := "127.0.0.1:" + strconv.Itoa(freePort(t))
		flagAddr := "127.0.0.1:" + strconv.Itoa(freePort(t))
		t.Setenv("RAVN_ADDR", envAddr)

		codeCh, _ := startRunServer(t, flagAddr, bootArgs(t, flagAddr)...) // --addr wins over RAVN_ADDR
		if conn, err := net.DialTimeout("tcp", envAddr, 200*time.Millisecond); err == nil {
			conn.Close()
			t.Errorf("server is bound to the env addr %s although the --addr flag %s should have won", envAddr, flagAddr)
		}
		if got := stopRunServer(t, codeCh, nil); got != 0 {
			t.Fatalf("run() exit = %d after SIGTERM, want 0", got)
		}
	})

	t.Run("token from environment gates the API", func(t *testing.T) {
		clearServerEnv(t)
		t.Setenv("RAVN_TOKEN", "env-secret")
		addr := "127.0.0.1:" + strconv.Itoa(freePort(t))

		codeCh, _ := startRunServer(t, addr, bootArgs(t, addr)...) // no --token flag at all
		defer func() { stopRunServer(t, codeCh, nil) }()

		if code, body := get(t, "http://"+addr+"/api/v1/projects"); code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/projects without bearer = %d, want 401 (body %s)", code, body)
		}
		code, body := reqWithAuth(t, "GET", "http://"+addr+"/api/v1/projects", "Bearer env-secret", "")
		if code != http.StatusOK {
			t.Errorf("GET /api/v1/projects with env token = %d, want 200 (body %s)", code, body)
		}
	})

	t.Run("no token anywhere leaves the API open", func(t *testing.T) {
		clearServerEnv(t) // RAVN_TOKEN stays empty: auth disabled (dev default)
		addr := "127.0.0.1:" + strconv.Itoa(freePort(t))

		codeCh, _ := startRunServer(t, addr, bootArgs(t, addr)...)
		defer func() { stopRunServer(t, codeCh, nil) }()

		if code, body := get(t, "http://"+addr+"/api/v1/projects"); code != http.StatusOK {
			t.Errorf("GET /api/v1/projects without auth = %d, want 200 (body %s)", code, body)
		}
	})
}

// TestServerRunLogLevelEnvPrecedence proves the nested env resolution for the log
// level: $LOG_LEVEL wins over $RAVN_LOG_LEVEL which beats the "info" default.
// An effective error level suppresses the info-level "ravn-server starting" line
// in the captured stderr buffer, which is how each branch is observed.
func TestServerRunLogLevelEnvPrecedence(t *testing.T) {
	cases := []struct {
		name         string
		logLevel     string // $LOG_LEVEL; "" = unset
		ravnLogLevel string // $RAVN_LOG_LEVEL; "" = unset
		wantStarting bool   // is the info-level startup line present?
	}{
		{name: "default info shows the startup line", logLevel: "", ravnLogLevel: "", wantStarting: true},
		{name: "LOG_LEVEL=error suppresses the info startup line", logLevel: "error", ravnLogLevel: "", wantStarting: false},
		{name: "RAVN_LOG_LEVEL used when LOG_LEVEL unset", logLevel: "", ravnLogLevel: "error", wantStarting: false},
		{name: "LOG_LEVEL beats RAVN_LOG_LEVEL", logLevel: "info", ravnLogLevel: "error", wantStarting: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearServerEnv(t)
			if tc.logLevel != "" {
				t.Setenv("LOG_LEVEL", tc.logLevel)
			}
			if tc.ravnLogLevel != "" {
				t.Setenv("RAVN_LOG_LEVEL", tc.ravnLogLevel)
			}

			addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
			codeCh, buf := startRunServer(t, addr, bootArgs(t, addr)...)
			defer func() { stopRunServer(t, codeCh, nil) }()

			seen := strings.Contains(buf.String(), "ravn-server starting")
			if seen != tc.wantStarting {
				t.Errorf("startup line present = %t, want %t; logs:\n%s", seen, tc.wantStarting, buf.String())
			}
		})
	}
}

// TestServerRunPortInUseSurfacesErrorAndStillShutsDown exercises the server-error
// branch of the run select: when the listen address is already taken, run() logs
// "server error", still closes broker + shuts the (never-listening) server down
// cleanly and returns 0 — the exit behavior of the former inline main body.
func TestServerRunPortInUseSurfacesErrorAndStillShutsDown(t *testing.T) {
	clearServerEnv(t)

	busy, err := net.Listen("tcp", "127.0.0.1:0") // hold a port so ListenAndServe fails fast
	if err != nil {
		t.Fatalf("reserve busy port: %v", err)
	}
	defer busy.Close()
	addr := busy.Addr().String()

	buf := &lockedBuffer{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- run(bootArgs(t, addr), buf) }()

	if got := waitRunExit(t, codeCh, buf); got != 0 {
		t.Fatalf("run() exit = %d on a busy port, want 0 (error is logged, not fatal); logs:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "server error") {
		t.Errorf("logs do not contain the server-error line; logs:\n%s", buf.String())
	}
}

// TestServerRunDBOpenFailureExitsOne pins the DB-open failure path: a data path
// whose parent cannot be created makes run() log and return 1 without ever
// listening (the sqlite package's MkdirAll of the parent directory fails).
func TestServerRunDBOpenFailureExitsOne(t *testing.T) {
	clearServerEnv(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	buf := &lockedBuffer{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- run(bootArgs(t, addr, "--data", filepath.Join(blocker, "nested", "ravn.db")), buf) }()

	if got := waitRunExit(t, codeCh, buf); got != 1 {
		t.Fatalf("run() exit = %d when the DB cannot be opened, want 1; logs:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "database open failed") {
		t.Errorf("logs do not contain the failure line; logs:\n%s", buf.String())
	}
}

// TestServerRunArtifactsDirFailureExitsOne pins the artifacts-dir creation
// failure path: a dir whose parent is a regular file cannot be created, so run()
// logs and returns 1 before touching the database or listening.
func TestServerRunArtifactsDirFailureExitsOne(t *testing.T) {
	clearServerEnv(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	buf := &lockedBuffer{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- run(bootArgs(t, addr, "--artifacts-dir", filepath.Join(blocker, "art")), buf) }()

	if got := waitRunExit(t, codeCh, buf); got != 1 {
		t.Fatalf("run() exit = %d when the artifacts dir cannot be created, want 1; logs:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "failed to create artifacts dir") {
		t.Errorf("logs do not contain the failure line; logs:\n%s", buf.String())
	}
}

// TestServerRunUnknownFlagExitsTwo pins flag-parse error handling: an unknown
// flag prints the FlagSet message + usage to stderr and run() returns 2 — the
// same exit code the global flag package used when it exited itself.
func TestServerRunUnknownFlagExitsTwo(t *testing.T) {
	clearServerEnv(t)

	buf := &lockedBuffer{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- run([]string{"--definitely-not-a-flag"}, buf) }()

	if got := waitRunExit(t, codeCh, buf); got != 2 {
		t.Fatalf("run() exit = %d on an unknown flag, want 2; output:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "flag provided but not defined") {
		t.Errorf("output does not name the unknown flag; output:\n%s", buf.String())
	}
}

// TestDefaultArtifactsDir pins the host-dependent artifacts default: /artifacts
// when that directory exists (the Docker layout), else ./artifacts for local
// development. The expectation is derived from the host so the test holds in
// both environments.
func TestDefaultArtifactsDir(t *testing.T) {
	want := "./artifacts"
	if fi, err := os.Stat("/artifacts"); err == nil && fi.IsDir() {
		want = "/artifacts"
	}
	if got := defaultArtifactsDir(); got != want {
		t.Errorf("defaultArtifactsDir() = %q, want %q (based on host state)", got, want)
	}
}

// TestEnvOr pins the env resolution helper: a set non-empty value wins, an unset
// or empty value falls back to the default.
func TestEnvOr(t *testing.T) {
	const probe = "RAVN_TEST_ENV_OR_PROBE"
	t.Setenv(probe, "") // cleared at top; cases override individually

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "set non-empty value wins", value: "from-env", want: "from-env"},
		{name: "empty value falls back to the default", value: "", want: "the-default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(probe, tc.value)
			if got := envOr(probe, "the-default"); got != tc.want {
				t.Errorf("envOr(%q, \"the-default\") = %q, want %q", probe, got, tc.want)
			}
		})
	}
}

// TestWriteJSONMarshalError pins the health-response error branch: an
// unmarshalable value (a channel fails json.Marshal) yields a 500 with the
// marshal failure named instead of panicking the handler. The happy path is
// asserted alongside as a sanity check on the shape used by /healthz.
func TestWriteJSONMarshalError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, make(chan int))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("writeJSON(marshal-failing value) status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "marshal health response") {
		t.Errorf("error body = %q, want it to name the marshal failure", rec.Body.String())
	}

	ok := httptest.NewRecorder()
	writeJSON(ok, http.StatusOK, healthResponse{Status: "ok", DB: "ok"})
	if ok.Code != http.StatusOK || ok.Body.String() != `{"status":"ok","db":"ok"}` {
		t.Errorf("writeJSON(happy path) = %d %q, want 200 %q", ok.Code, ok.Body.String(), `{"status":"ok","db":"ok"}`)
	}
	if ct := ok.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
