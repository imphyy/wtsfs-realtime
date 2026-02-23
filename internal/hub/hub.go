package hub

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/williamhunt/wtsfs-realtime/internal/persist"
)

// Hub manages all active campaign sessions.
// Thread-safe — sessions is protected by a RWMutex.
type Hub struct {
	mu       sync.RWMutex
	sessions map[string]*Session // campaignID → *Session
	store    *persist.Store
}

func New(store *persist.Store) *Hub {
	return &Hub{
		sessions: make(map[string]*Session),
		store:    store,
	}
}

// GetOrCreateSession returns the session for a campaign, creating one if it doesn't exist.
// On creation it loads persisted state from Postgres.
func (h *Hub) GetOrCreateSession(campaignID string) (*Session, error) {
	// Fast path — session already exists
	h.mu.RLock()
	if s, ok := h.sessions[campaignID]; ok {
		h.mu.RUnlock()
		return s, nil
	}
	h.mu.RUnlock()

	// Slow path — create new session
	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if s, ok := h.sessions[campaignID]; ok {
		return s, nil
	}

	// Load last persisted state from Postgres (or start fresh)
	freshState := SessionState{
		CampaignID: campaignID,
		Tokens:     make(map[string]*Token),
		GridSize:   64,
	}

	raw, err := h.store.Load(campaignID)
	if err != nil {
		slog.Warn("could not load session state, starting fresh", "campaign", campaignID, "err", err)
	} else if raw != nil {
		if jsonErr := json.Unmarshal(raw, &freshState); jsonErr != nil {
			slog.Warn("could not decode session state, starting fresh", "campaign", campaignID, "err", jsonErr)
		}
		if freshState.Tokens == nil {
			freshState.Tokens = make(map[string]*Token)
		}
		if freshState.SavedMaps == nil {
			freshState.SavedMaps = make(map[string]*SavedMap)
		}
	}

	s := newSession(campaignID, freshState, h.store)
	h.sessions[campaignID] = s
	go s.run()

	slog.Info("session created", "campaign", campaignID)
	return s, nil
}

// JoinSession registers a client with a campaign session.
func (h *Hub) JoinSession(campaignID string, client *Client) error {
	s, err := h.GetOrCreateSession(campaignID)
	if err != nil {
		return err
	}
	client.session = s
	s.register <- client
	return nil
}

// CloseSession shuts down a session if it exists.
func (h *Hub) CloseSession(campaignID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.sessions[campaignID]; ok {
		s.close()
		delete(h.sessions, campaignID)
		slog.Info("session closed", "campaign", campaignID)
	}
}

// CloseIdleSessions shuts down sessions with no connected clients.
// Call this periodically to free memory.
func (h *Hub) CloseIdleSessions() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for id, s := range h.sessions {
		if s.clientCount() == 0 {
			s.close()
			delete(h.sessions, id)
			slog.Info("idle session closed", "campaign", id)
		}
	}
}

// BroadcastMessage persists a message and broadcasts it to all connected clients
// in the campaign session (if one is active). Used by the internal HTTP API so
// that Next.js server actions can trigger real-time events without a WebSocket.
// If no session is active the message is still saved to DB — clients will see it
// in their history on next connect.
func (h *Hub) BroadcastMessage(campaignID, userID, messageType string, content []byte, gmOnly bool, recipientID, characterID string) error {
	saved, err := h.store.SaveChatMessage(userID, campaignID, messageType, json.RawMessage(content), gmOnly, recipientID, characterID)
	if err != nil {
		return err
	}

	// Only broadcast if there's an active session with connected clients.
	h.mu.RLock()
	s, ok := h.sessions[campaignID]
	h.mu.RUnlock()
	if !ok {
		return nil
	}

	outPayload, err := json.Marshal(ChatMessagePayload{
		ID:          saved.ID,
		CampaignID:  saved.CampaignID,
		UserID:      saved.UserID,
		Username:    saved.Username,
		CharacterID: saved.CharacterID,
		RecipientID: saved.RecipientID,
		MessageType: saved.MessageType,
		Content:     saved.Content,
		GmOnly:      saved.GmOnly,
		CreatedAt:   saved.CreatedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	outMsg := Message{
		Type:       TypeChatMessage,
		CampaignID: campaignID,
		UserID:     userID,
		Payload:    json.RawMessage(outPayload),
	}

	if gmOnly {
		s.broadcastToGMs(outMsg)
	} else if recipientID != "" {
		s.broadcastWhisper(outMsg, userID, recipientID)
	} else {
		s.broadcast(outMsg, nil)
	}

	return nil
}

// Stats returns a snapshot of active session/client counts (for health endpoint).
func (h *Hub) Stats() map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stats := make(map[string]int, len(h.sessions))
	for id, s := range h.sessions {
		stats[id] = s.clientCount()
	}
	return stats
}
