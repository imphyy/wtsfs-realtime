package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/williamhunt/wtsfs-realtime/internal/hub"
)

// BroadcastRequest is the body expected by POST /internal/broadcast.
type BroadcastRequest struct {
	CampaignID  string          `json:"campaign_id"`
	UserID      string          `json:"user_id"`
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`
	GmOnly      bool            `json:"gm_only"`
	RecipientID string          `json:"recipient_id,omitempty"`
	CharacterID string          `json:"character_id,omitempty"`
}

// InternalHandler handles POST /internal/broadcast.
// Protected by a shared secret in the Authorization header:
//   Authorization: Bearer <INTERNAL_API_SECRET>
func InternalHandler(h *hub.Hub, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify shared secret
		if r.Header.Get("Authorization") != "Bearer "+secret {
			slog.Warn("internal broadcast: unauthorized", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req BroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		if req.CampaignID == "" || req.UserID == "" || req.MessageType == "" || len(req.Content) == 0 {
			http.Error(w, "campaign_id, user_id, message_type, and content are required", http.StatusBadRequest)
			return
		}

		if err := h.BroadcastMessage(
			req.CampaignID,
			req.UserID,
			req.MessageType,
			[]byte(req.Content),
			req.GmOnly,
			req.RecipientID,
			req.CharacterID,
		); err != nil {
			slog.Error("internal broadcast failed", "err", err, "campaign", req.CampaignID)
			http.Error(w, "broadcast failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
