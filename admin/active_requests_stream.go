package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const activeRequestStreamHeartbeatInterval = 15 * time.Second

type activeRequestStreamSnapshot struct {
	ActiveRequests       int                            `json:"active_requests"`
	ActiveRequestDetails []runtimeActiveRequestResponse `json:"active_request_details"`
}

// StreamActiveRequests streams complete snapshots whenever active request state changes.
func (h *Handler) StreamActiveRequests(c *gin.Context) {
	if h == nil || h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "active request tracking is unavailable"})
		return
	}

	changes, unsubscribe := h.store.SubscribeActiveRequestChanges()
	defer unsubscribe()

	headers := c.Writer.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache, no-store")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")

	if err := h.writeActiveRequestSnapshot(c.Writer); err != nil {
		return
	}

	heartbeat := time.NewTicker(activeRequestStreamHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-changes:
			if err := h.writeActiveRequestSnapshot(c.Writer); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func (h *Handler) writeActiveRequestSnapshot(w gin.ResponseWriter) error {
	details := h.runtimeActiveRequestDetails(time.Now())
	payload, err := json.Marshal(activeRequestStreamSnapshot{
		ActiveRequests:       len(details),
		ActiveRequestDetails: details,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload); err != nil {
		return err
	}
	w.Flush()
	return nil
}
