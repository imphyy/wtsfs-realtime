package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/williamhunt/wtsfs-realtime/internal/auth"
	"github.com/williamhunt/wtsfs-realtime/internal/hub"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins — the Next.js app can be on any domain.
	// In production, restrict this to your actual frontend domain.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Handler handles the HTTP → WebSocket upgrade and wires up a new Client.
type Handler struct {
	hub      *hub.Hub
	verifier *auth.Verifier
	// gmUserIDs is the set of Clerk user IDs that are GMs.
	// Populated from the CAMPAIGN_GM_IDS env var or fetched from the DB.
	// For now we trust the ?gm=true query param only after JWT validation.
	// You can hook this into your Postgres campaigns table later.
	isGMFunc func(userID, campaignID string) bool
}

func NewHandler(h *hub.Hub, v *auth.Verifier, isGMFunc func(userID, campaignID string) bool) *Handler {
	return &Handler{hub: h, verifier: v, isGMFunc: isGMFunc}
}

// ServeHTTP handles GET /ws?campaign=<id>&token=<clerk_jwt>
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Extract and verify Clerk JWT
	tokenStr, err := auth.ExtractToken(r)
	if err != nil {
		http.Error(w, "missing auth token", http.StatusUnauthorized)
		return
	}

	claims, err := h.verifier.Verify(r.Context(), tokenStr)
	if err != nil {
		slog.Warn("invalid token", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// 2. Extract campaign ID
	campaignID := r.URL.Query().Get("campaign")
	if campaignID == "" {
		http.Error(w, "missing campaign query param", http.StatusBadRequest)
		return
	}

	// 3. Determine GM status
	isGM := h.isGMFunc(claims.UserID, campaignID)

	// 4. Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "err", err)
		return
	}

	// 5. Create client and join session
	client := &hub.Client{
		UserID:     claims.UserID,
		CampaignID: campaignID,
		IsGM:       isGM,
		Conn:       conn,
		Send:       make(chan []byte, 256),
	}

	if err := h.hub.JoinSession(campaignID, client); err != nil {
		slog.Error("failed to join session", "campaign", campaignID, "err", err)
		conn.Close()
		return
	}

	slog.Info("client connected", "user", claims.UserID, "campaign", campaignID, "gm", isGM)

	// 6. Start read/write pumps (each in their own goroutine)
	go client.WritePump()
	go client.ReadPump()
}

// HealthHandler returns a JSON summary of active sessions/clients.
func HealthHandler(h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := h.Stats()
		totalClients := 0
		for _, count := range stats {
			totalClients += count
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":        "ok",
			"sessions":      len(stats),
			"total_clients": totalClients,
			"sessions_detail": stats,
		})
	}
}
