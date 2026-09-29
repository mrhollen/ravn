package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
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
			"error":   "invalid input",
			"message": "missing required query parameter \"project_id\"",
		})
		return
	}
	id := domain.ID(raw)
	a.streamEvents(w, r, id)
}

func (a *App) streamEvents(w http.ResponseWriter, r *http.Request, projectID domain.ID) {
	// Verify that the requested project exists
	if _, err := a.Projects.Get(r.Context(), projectID); err != nil {
		writeAppError(w, err, "project", projectID)
		return
	}

	if _, ok := w.(http.Flusher); !ok {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": "streaming unsupported",
		})
		return
	}

	rc := http.NewResponseController(w)

	// Per-write deadline: a stalled/slow client whose TCP buffers fill can block
	// the handler INSIDE a write, outside the select below. Bounding every
	// write/flush to writeTimeout guarantees the blocked call returns an error
	// so we exit and run deferred cleanup instead of zombie-ing.
	const writeTimeout = 10 * time.Second

	setWriteDeadline := func() {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	}
	clearWriteDeadline := func() {
		_ = rc.SetWriteDeadline(time.Time{})
	}

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

	events, cancel := a.Broker.Subscribe(projectID)
	defer cancel()

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

	// Initial comment confirming stream readiness
	if err := writeChunk(": connected\n\n"); err != nil {
		return
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
			dto := toRealtimeEventDTO(ev)
			data, err := json.Marshal(dto)
			if err != nil {
				continue
			}
			msg := fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", string(ev.ID), string(ev.Kind), string(data))
			if err := writeChunk(msg); err != nil {
				return
			}
		}
	}
}
