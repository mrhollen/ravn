package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// handleProjectEvents streams realtime SSE events for a project identified by {id} in path.
// GET /api/v1/projects/{id}/events
func (a *App) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a.streamEvents(w, r, id)
}

// handleEvents streams realtime SSE events scoped to the project_id query parameter.
// GET /api/v1/events?project_id=...
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("project_id")
	if raw == "" {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": "missing required query parameter \"project_id\"",
		})
		return
	}
	id, ok := parseBoundaryID(w, raw) // boundary format check before the broker/DB read
	if !ok {
		return
	}
	a.streamEvents(w, r, id)
}

func (a *App) streamEvents(w http.ResponseWriter, r *http.Request, projectID domain.ID) {
	// Verify the requested project exists AND — under membership scoping — that the
	// requesting identity can SEE it. This gate runs BEFORE any SSE header is set, the
	// 200 status is written, or a single frame / replayed event is emitted: a non-member
	// gets a JSON 403 and the stream is never opened (T5 AC1/AC2 — zero event leakage).
	// checkProject reuses the T4 visibility+authorization gate verbatim: in DEFAULT/global
	// mode it short-circuits to allow with ZERO extra lookups, so the fetch keeps its
	// pre-T5 404 behavior and an allowed stream is byte-identical (global mode is a
	// guaranteed no-op — existing SSE specs stay green). Membership mode resolves the
	// acting subject's role ONCE and writes 403 before anything below runs.
	p, err := a.Projects.Get(r.Context(), projectID)
	if err != nil {
		writeAppError(w, err, "project", projectID)
		return
	}
	if _, _, ok := a.checkProject(w, r, "", p, domain.OpRead); !ok {
		return // 403 already written by checkProject; no frames/replay below are reached
	}

	if _, ok := w.(http.Flusher); !ok {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": "streaming unsupported",
		})
		return
	}

	rc := http.NewResponseController(w)

	// Per-write deadline: a stalled/slow client whose TCP buffers fill can block
	// the handler INSIDE a write, outside the select below. Bounding every
	// write/flush to a.SSEWriteDeadline guarantees the blocked call returns an
	// error so we exit and run deferred cleanup instead of zombie-ing. The
	// deadline is wired through Deps (default: DefaultSSEWriteDeadline — 10s in
	// production); tests override it with short values, not this handler.

	// sseReplayCap bounds how many missed events a reconnecting client replays
	// in one burst: a long-offline client must not pull an unbounded backlog
	// into its first read. Anything beyond the cap is still resynced — on the
	// next reconnect (its cursor advances) or a refresh.
	const sseReplayCap = 5000

	setWriteDeadline := func() {
		_ = rc.SetWriteDeadline(time.Now().Add(a.SSEWriteDeadline))
	}
	clearWriteDeadline := func() {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	// Subscribe BEFORE the response headers leave: a client may fire its first
	// mutation the instant it receives the 200, and an event published in the
	// header-to-subscribe window would reach zero subscribers and be lost
	// (lost-wakeup). With subscribe-first program order, any publish triggered
	// after the client has the headers is guaranteed to find this subscription;
	// events landing during replay below are buffered in `events`, never dropped.
	// DO NOT reorder this below the WriteHeader/Flush — the ordering canary
	// TestEvents_SSE_SubscribeVisibleWhenHeadersReachClient fails fast if you do.
	events, cancel := a.Broker.Subscribe(projectID)
	defer cancel()

	// SSE response headers (guarded by the same per-write deadline as below)
	setWriteDeadline()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	clearWriteDeadline()

	writeChunk := func(msg string) error {
		setWriteDeadline()
		if _, err := fmt.Fprint(w, msg); err != nil {
			return err
		}
		if err := rc.Flush(); err != nil {
			return err
		}
		clearWriteDeadline()
		return nil
	}

	// formatEventFrame renders one event as a DEFAULT (unnamed) SSE message
	// frame — an `id:` line plus a single `data:` JSON payload, with NO
	// `event:` line. The routing field is the payload's "kind" member
	// (realtimeEventDTO.Kind), so every client dispatches by parsing the JSON
	// instead of pre-registering one EventSource listener per kind. That
	// matters because per the WHATWG spec a frame carrying `event: <name>` is
	// delivered ONLY to listeners registered for exactly that name —
	// onmessage never fires for named frames — so any server-side addition of
	// a new kind would have been silently dropped by every client whose
	// whitelist predated it. With unnamed frames, one onmessage handler sees
	// everything and a brand-new kind keeps the UI in sync without a client
	// code change. Marshal failures are reported to the caller (which skips
	// that event and keeps streaming); I/O failures surface through writeChunk
	// and end the stream.
	formatEventFrame := func(ev domain.Event) (string, error) {
		dto := toRealtimeEventDTO(ev)
		data, err := json.Marshal(dto)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("id: %s\ndata: %s\n\n", string(ev.ID), string(data)), nil
	}

	// Initial comment confirming stream readiness
	if err := writeChunk(": connected\n\n"); err != nil {
		return
	}

	// --- Last-Event-ID replay (SSE reconnect) --------------------------------
	// Per the WHATWG SSE spec, a client that reconnects after a drop re-sends
	// the id of the last frame it received — as the Last-Event-ID header when
	// the browser's native EventSource auto-reconnects, or as the last_event_id
	// query parameter on MANUAL reconnects (e.g. the web app's backoff loop).
	// The header takes precedence over the query parameter. Missed events are
	// replayed from the durable event log BEFORE live forwarding starts so a
	// client resyncs without missing anything or doing a full reload.
	//
	// Subscribe-first ordering (above): we subscribe to the broker BEFORE
	// reading the DB, so events published during the replay window are buffered
	// in `events` instead of lost; `replayed` records every id already emitted
	// so those live events are never sent twice. An invalid or unwired cursor
	// degrades to live-only (a log line at most) — it never errors the
	// connection, because the stream must still connect.
	rawCursor := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if rawCursor == "" {
		rawCursor = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	var replayed map[domain.ID]struct{} // ids already emitted to this client (replay window)
	if rawCursor != "" {
		if !domain.ID(rawCursor).Valid() {
			slog.Warn("sse: ignoring invalid Last-Event-ID; serving live-only",
				"value", rawCursor, "project", projectID.String())
		} else if a.Events == nil {
			slog.Warn("sse: event store not wired; cannot replay Last-Event-ID",
				"cursor", rawCursor, "project", projectID.String())
		} else {
			// Stored ids are uppercase ULIDs; a lowercase cursor is tolerated
			// (normalized) so non-browser clients round-trip.
			cursor := domain.ID(strings.ToUpper(rawCursor))
			missed, err := a.Events.ListAfterProject(r.Context(), projectID, cursor, sseReplayCap)
			if err != nil {
				slog.Error("sse: replay read failed; serving live-only",
					"err", err, "cursor", string(cursor), "project", projectID.String())
			} else {
				replayed = make(map[domain.ID]struct{}, len(missed)+8)
				for _, ev := range missed {
					frame, ferr := formatEventFrame(ev)
					if ferr != nil {
						continue // unserializable payload: skip it, keep the stream alive
					}
					if err := writeChunk(frame); err != nil {
						return
					}
					replayed[ev.ID] = struct{}{}
				}
			}
		}
	}

	// 15-second heartbeat to keep connection alive through proxies
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := writeChunk(": ping\n\n"); err != nil {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			// Dedupe against the replay: a live event that also appeared in the
			// DB replay slice (published during the replay window) was already
			// emitted above; every live event not in the slice is new, so id
			// membership in `replayed` — fixed once the replay ends — is the
			// complete dedupe test.
			if replayed != nil {
				if _, dup := replayed[ev.ID]; dup {
					continue
				}
			}
			frame, err := formatEventFrame(ev)
			if err != nil {
				continue
			}
			if err := writeChunk(frame); err != nil {
				return
			}
		}
	}
}
