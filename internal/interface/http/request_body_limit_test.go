package httpapi

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildTaskBody returns a fully VALID POST /tasks JSON body of EXACTLY total
// bytes. The bulk lives in acceptance_criteria: each text is capped at
// domain.MaxACTextLen (500 runes) but the COUNT of initial criteria is not, so
// every per-field bound is respected while the body reaches arbitrary sizes.
// (A project-creation body can never be exactly 1MB — name ≤ 100 and
// description ≤ 64KB — so the boundary is exercised on this endpoint instead.)
func buildTaskBody(t *testing.T, projectID string, total int) string {
	t.Helper()
	const fullAC = 500 // domain.MaxACTextLen: a valid text exactly at the cap
	prefix := `{"actor":{"name":"eve","type":"human"},"project_id":"` + projectID + `","title":"Boundary Task","acceptance_criteria":[`
	suffix := "]}"
	base := len(prefix) + len(suffix)

	// total = base + k full elements (each 2 quotes + fullAC text + 1 comma)
	//         + one tail element (2 quotes + L text). Solve for the tail length.
	// The valid window [1,fullAC] is narrower than the per-element stride
	// (fullAC+3), so at most one k can ever fit — pick it directly.
	k := (total - base - 2) / (fullAC + 3)
	l := total - base - 2 - k*(fullAC+3)
	if l < 1 || l > fullAC {
		t.Fatalf("cannot construct a valid %d-byte task body: no tail length in [1,%d] fits (k=%d l=%d)", total, fullAC, k, l)
	}
	fullText := strings.Repeat("a", fullAC)
	elems := make([]string, k+1)
	for i := range elems {
		if i < k {
			elems[i] = `"` + fullText + `"`
		} else {
			elems[i] = `"` + strings.Repeat("a", l) + `"`
		}
	}
	body := prefix + strings.Join(elems, ",") + suffix
	if len(body) != total {
		t.Fatalf("crafted body is %d bytes, want exactly %d", len(body), total)
	}
	return body
}

// TestRequestBodyLimit_ExactBoundary proves the JSON body cap is inclusive: a
// request of EXACTLY maxJSONBodyBytes bytes succeeds (the byte limiter must
// let a full-limit stream through cleanly), and one extra byte gets HTTP 413
// Payload Too Large — not a 400 "malformed JSON" from silent truncation.
func TestRequestBodyLimit_ExactBoundary(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	proj := createProject(t, stk, "Boundary Proj")

	raw := buildTaskBody(t, proj.ID, maxJSONBodyBytes)
	code, body := doRaw(t, stk.Handler, "POST", "/api/v1/tasks", raw)
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks with %d-byte valid body = %d, want 201 (body: %.300s)", len(raw), code, string(body))
	}

	oversized := buildTaskBody(t, proj.ID, maxJSONBodyBytes+1)
	code, body = doRaw(t, stk.Handler, "POST", "/api/v1/tasks", oversized)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /tasks with %d-byte valid body = %d, want 413 (body: %.300s)", len(oversized), code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "payload too large" {
		t.Errorf("error = %q, want 'payload too large'", e.Error)
	}
	if !strings.Contains(e.Message, "1048576") {
		t.Errorf("message = %q, want it to mention the %d byte limit", e.Message, maxJSONBodyBytes)
	}
}

// TestRequestBodyLimit_OversizedValidJSON_Returns413NotMalformed drives both
// decode paths with VALID JSON that exceeds the cap: they must fail with 413
// "payload too large", never with the old LimitReader symptom of a 400
// "malformed JSON body: unexpected EOF" caused by truncation.
func TestRequestBodyLimit_OversizedValidJSON_Returns413NotMalformed(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	// decodeJSON path (POST /projects): a valid JSON object padded to ~1.3MB.
	raw := `{"actor":{"name":"ada","type":"human"},"name":"Big Proj","description":"` + strings.Repeat("a", 1<<20+256) + `"}`
	code, body := doRaw(t, stk.Handler, "POST", "/api/v1/projects", raw)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /projects with %d-byte valid JSON = %d, want 413 (body: %s)", len(raw), code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "payload too large" {
		t.Errorf("error = %q, want 'payload too large'", e.Error)
	}
	if strings.Contains(e.Message, "malformed JSON") {
		t.Errorf("message = %q: old LimitReader behavior (truncated into 400 'malformed JSON') regressed", e.Message)
	}

	// decodeOptionalJSON path: DELETE an AC with a raw JSON body > 1MB. ("note"
	// is not even a field of acRemoveReq — the oversized read must fail BEFORE
	// any decoding happens.)
	proj := createProject(t, stk, "Body Limit Proj")
	task := newTask(t, stk, proj.ID, "Body Limit Task")
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
		"actor": actor("ada"), "text": "First AC",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup AC = %d, want 201", code)
	}

	rawRemove := `{"actor":{"name":"ada","type":"human"},"note":"` + strings.Repeat("a", 1<<20+64) + `"}`
	code, body = doRaw(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/1", rawRemove)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("DELETE AC with %d-byte body = %d, want 413 (body: %s)", len(rawRemove), code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "payload too large" {
		t.Errorf("error = %q, want 'payload too large'", e.Error)
	}

	// decodeJSON on a different endpoint: POST /tasks with the title just over 1MB.
	rawTask := `{"actor":{"name":"eve","type":"human"},"project_id":"` + proj.ID + `","title":"` + strings.Repeat("a", 1<<20+16) + `"}`
	code, body = doRaw(t, stk.Handler, "POST", "/api/v1/tasks", rawTask)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /tasks with %d-byte body = %d, want 413 (body: %s)", len(rawTask), code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "payload too large" {
		t.Errorf("error = %q, want 'payload too large'", e.Error)
	}
}

// TestRequestBodyLimit_UnboundedStream_ConnectionClosed_NoDrain verifies net/http's
// requestTooLarge behavior over a real TCP connection: an oversized,
// never-terminated chunked stream must get 413 with Connection: close, and the
// server must then close the connection immediately instead of consuming the
// remaining chunks. (With the old io.LimitReader code this test would see
// status 400 "malformed JSON body: unexpected EOF", NO Connection: close, and
// the server blocking on reads — draining the remaining ~1.5MB of chunks in the
// background.) Intentionally not t.Parallel(): raw TCP + no drain assertions.
func TestRequestBodyLimit_UnboundedStream_ConnectionClosed_NoDrain(t *testing.T) {
	stk := newStack(t)
	ts := httptest.NewServer(stk.Handler)
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()

	head := "POST /api/v1/projects HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Content-Type: application/json\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"\r\n"
	if _, err := conn.Write([]byte(head)); err != nil {
		t.Fatalf("write request head: %v", err)
	}

	// ~2.5MB of a never-terminated JSON string (the "description" value keeps
	// going), sent as real 64KB chunks — and the terminating 0\r\n\r\n chunk is
	// deliberately NEVER sent, so the stream stays open like an unbounded
	// oversized client.
	open := []byte(`{"name":"big","description":"`)
	const total = 2<<20 + 64<<10 // 2.5MB keeps this test fast
	payload := make([]byte, 0, total)
	payload = append(payload, open...)
	payload = append(payload, strings.Repeat("a", total-len(open))...)
	const chunkSize = 64 << 10

	// The server stops reading after maxJSONBodyBytes and closes the connection,
	// so later writes can hit ECONNRESET — that is EXPECTED evidence of the no-
	// drain close, so stream in a goroutine and never fail on write errors.
	go func() {
		for i := 0; i < len(payload); i += chunkSize {
			end := i + chunkSize
			if end > len(payload) {
				end = len(payload)
			}
			chunk := payload[i:end]
			// Chunk framing per RFC 9112: hex size, CRLF, data, CRLF.
			framed := append([]byte(strconv.FormatInt(int64(len(chunk)), 16)), '\r', '\n')
			framed = append(framed, chunk...)
			framed = append(framed, '\r', '\n')
			conn.Write(framed) // errors expected once the server closes; ignored by design
		}
	}()

	// Byte-at-a-time line reader straight on the socket (no bufio): any bytes we
	// do not consume must stay on the wire, so the post-response read below
	// truthfully observes "connection closed" rather than buffered leftovers.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	readLine := func() (string, error) {
		var sb strings.Builder
		for {
			b := make([]byte, 1)
			if _, err := io.ReadFull(conn, b); err != nil {
				return "", err
			}
			if b[0] == '\n' {
				return strings.TrimRight(sb.String(), "\r"), nil
			}
			sb.WriteByte(b[0])
		}
	}

	statusLine, err := readLine()
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, " 413 ") {
		t.Fatalf("status line = %q, want HTTP/1.1 413 Request Entity Too Large", statusLine)
	}

	headers := map[string]string{}
	for {
		line, err := readLine()
		if err != nil {
			t.Fatalf("read response headers: %v", err)
		}
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		headers[strings.ToLower(k)] = v
	}
	if got := headers["connection"]; got != "close" {
		t.Errorf("Connection header = %q, want 'close' (net/http requestTooLarge marks the connection to close)", got)
	}

	// Consume the fixed-length 413 response body so the post-response read
	// below starts exactly at the end of the stream.
	if cl := headers["content-length"]; cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < 0 {
			t.Fatalf("parse Content-Length %q: %v", cl, err)
		}
		drain := make([]byte, n)
		if _, err := io.ReadFull(conn, drain); err != nil {
			t.Fatalf("read 413 response body (%d bytes): %v", n, err)
		}
	}

	// The server closed right after the full 413 response, so a further read
	// must fail promptly instead of blocking while it drains our remaining
	// chunks. Both close styles are correct outcomes: FIN → io.EOF (nothing
	// unread left in the kernel buffer), or RST → "connection reset by peer"
	// (Linux sends RST when close()ing a socket that still has unread buffered
	// data — exactly our no-drain scenario, where ~1.5MB of our chunks may
	// never have been read). A deadline timeout instead would mean the server
	// was still keeping the connection open, draining.
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set short read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	_, readErr := conn.Read(buf)
	promptTearDown := readErr == io.EOF || (readErr != nil && strings.Contains(strings.ToLower(readErr.Error()), "connection reset"))
	if !promptTearDown {
		t.Errorf("read after response = %v; want io.EOF or ECONNRESET (server closed the connection promptly instead of draining the oversized stream)", readErr)
	}
}
