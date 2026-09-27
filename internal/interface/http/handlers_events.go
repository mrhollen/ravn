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

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": "streaming unsupported",
		})
		return
	}

	// SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := a.Broker.Subscribe(projectID)
	defer cancel()

	// Initial comment confirming stream readiness
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// 15-second heartbeat to keep connection alive through proxies
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			dto := toRealtimeEventDTO(ev)
			data, err := json.Marshal(dto)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", string(ev.ID), string(ev.Kind), string(data))
			flusher.Flush()
		}
	}
}
