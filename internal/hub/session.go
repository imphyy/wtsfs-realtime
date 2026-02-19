package hub

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/williamhunt/wtsfs-realtime/internal/persist"
)

const (
	flushInterval    = 5 * time.Second
	clientBufferSize = 256
	chatHistoryLimit = 100
)

// Session manages all state for one campaign's VTT session.
// A single goroutine (run) owns the state — no per-message locking needed.
type Session struct {
	campaignID string
	state      SessionState
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	in         chan ClientMessage
	quit       chan struct{}
	store      *persist.Store
}

func newSession(campaignID string, initialState SessionState, store *persist.Store) *Session {
	return &Session{
		campaignID: campaignID,
		state:      initialState,
		clients:    make(map[*Client]bool),
		register:   make(chan *Client, 8),
		unregister: make(chan *Client, 8),
		in:         make(chan ClientMessage, 256),
		quit:       make(chan struct{}),
		store:      store,
	}
}

// run is the single event loop for this session. Must be called in a goroutine.
func (s *Session) run() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case client := <-s.register:
			s.clients[client] = true
			slog.Info("client joined session", "user", client.UserID, "campaign", s.campaignID, "total", len(s.clients))
			// Send full current VTT state to the new client
			s.sendStateSyncTo(client)
			// Send recent chat history to the new client
			s.sendChatHistoryTo(client)

		case client := <-s.unregister:
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				close(client.Send)
				slog.Info("client left session", "user", client.UserID, "campaign", s.campaignID, "total", len(s.clients))
			}

		case cm := <-s.in:
			s.handleMessage(cm)

		case <-ticker.C:
			if err := s.store.Flush(s.campaignID, s.state); err != nil {
				slog.Error("flush failed", "campaign", s.campaignID, "err", err)
			}

		case <-s.quit:
			// Final flush before shutdown
			if err := s.store.Flush(s.campaignID, s.state); err != nil {
				slog.Error("final flush failed", "campaign", s.campaignID, "err", err)
			}
			// Close all client channels
			for client := range s.clients {
				close(client.Send)
			}
			return
		}
	}
}

func (s *Session) handleMessage(cm ClientMessage) {
	msg := cm.message

	switch msg.Type {

	// --- Chat ---

	case TypeChatSend:
		var p ChatSendPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "chat.send requires valid payload")
			return
		}

		// Enforce GM-only: only GMs can set gm_only
		if p.GmOnly && !cm.client.IsGM {
			p.GmOnly = false
		}

		// Persist to DB
		saved, err := s.store.SaveChatMessage(
			cm.client.UserID,
			s.campaignID,
			p.MessageType,
			p.Content,
			p.GmOnly,
			p.RecipientID,
			p.CharacterID,
		)
		if err != nil {
			slog.Error("failed to save chat message", "err", err, "user", cm.client.UserID)
			s.sendError(cm.client, "db_error", "failed to save message")
			return
		}

		// Build broadcast payload
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
			slog.Error("failed to marshal chat message", "err", err)
			return
		}

		outMsg := Message{
			Type:       TypeChatMessage,
			CampaignID: s.campaignID,
			UserID:     cm.client.UserID,
			Payload:    json.RawMessage(outPayload),
		}

		// Broadcast with visibility rules:
		// - gm_only: only send to GMs
		// - whisper (recipient_id set): only send to sender and recipient
		// - otherwise: broadcast to all
		if saved.GmOnly {
			s.broadcastToGMs(outMsg)
		} else if saved.RecipientID != "" {
			s.broadcastWhisper(outMsg, cm.client.UserID, saved.RecipientID)
		} else {
			s.broadcast(outMsg, nil)
		}

	// --- VTT ---

	case TypeTokenAdd:
		var p TokenAddPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "token.add requires valid payload")
			return
		}
		if s.state.Tokens == nil {
			s.state.Tokens = make(map[string]*Token)
		}
		s.state.Tokens[p.TokenID] = &Token{
			TokenID:     p.TokenID,
			CharacterID: p.CharacterID,
			Name:        p.Name,
			ImageURL:    p.ImageURL,
			X:           p.X,
			Y:           p.Y,
			Width:       p.Width,
			Height:      p.Height,
			Visible:     p.Visible,
		}
		s.broadcast(msg, nil)

	case TypeTokenMove:
		var p TokenMovePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "token.move requires valid payload")
			return
		}
		token, ok := s.state.Tokens[p.TokenID]
		if !ok {
			s.sendError(cm.client, "not_found", "token not found")
			return
		}
		token.X = p.X
		token.Y = p.Y
		s.broadcast(msg, nil)

	case TypeTokenRemove:
		var p TokenRemovePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "token.remove requires valid payload")
			return
		}
		delete(s.state.Tokens, p.TokenID)
		s.broadcast(msg, nil)

	case TypeTokenUpdate:
		var p TokenUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "token.update requires valid payload")
			return
		}
		token, ok := s.state.Tokens[p.TokenID]
		if !ok {
			s.sendError(cm.client, "not_found", "token not found")
			return
		}
		if p.Name != nil {
			token.Name = *p.Name
		}
		if p.ImageURL != nil {
			token.ImageURL = *p.ImageURL
		}
		if p.Width != nil {
			token.Width = *p.Width
		}
		if p.Height != nil {
			token.Height = *p.Height
		}
		if p.Visible != nil {
			if cm.client.IsGM {
				token.Visible = *p.Visible
			} else {
				s.sendError(cm.client, "forbidden", "only GMs can change token visibility")
				return
			}
		}
		s.broadcast(msg, nil)

	case TypeFogUpdate:
		if !cm.client.IsGM {
			s.sendError(cm.client, "forbidden", "only GMs can update fog of war")
			return
		}
		var p FogUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "fog.update requires valid payload")
			return
		}
		s.state.FogZones = p.Zones
		s.broadcast(msg, nil)

	case TypeMapSet:
		if !cm.client.IsGM {
			s.sendError(cm.client, "forbidden", "only GMs can change the map")
			return
		}
		var p MapSetPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			s.sendError(cm.client, "invalid_payload", "map.set requires valid payload")
			return
		}
		s.state.MapID = p.MapID
		s.state.MapURL = p.ImageURL
		s.state.GridSize = p.GridSize
		s.state.MapWidth = p.Width
		s.state.MapHeight = p.Height
		s.state.Tokens = make(map[string]*Token)
		s.state.FogZones = nil
		s.broadcast(msg, nil)

	case TypePing:
		cm.client.sendMessage(Message{Type: TypePong, CampaignID: s.campaignID})

	default:
		slog.Warn("unknown message type", "type", msg.Type, "user", cm.client.UserID)
	}
}

// broadcast sends a message to all clients, optionally skipping one.
func (s *Session) broadcast(msg Message, skip *Client) {
	raw, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal broadcast message", "err", err)
		return
	}
	for client := range s.clients {
		if client == skip {
			continue
		}
		select {
		case client.Send <- raw:
		default:
			slog.Warn("slow client dropped", "user", client.UserID)
			close(client.Send)
			delete(s.clients, client)
		}
	}
}

// broadcastToGMs sends a message only to GM clients.
func (s *Session) broadcastToGMs(msg Message) {
	raw, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal GM message", "err", err)
		return
	}
	for client := range s.clients {
		if !client.IsGM {
			continue
		}
		select {
		case client.Send <- raw:
		default:
			slog.Warn("slow GM client dropped", "user", client.UserID)
			close(client.Send)
			delete(s.clients, client)
		}
	}
}

// broadcastWhisper sends a message only to the sender and recipient.
func (s *Session) broadcastWhisper(msg Message, senderID, recipientID string) {
	raw, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal whisper message", "err", err)
		return
	}
	for client := range s.clients {
		if client.UserID != senderID && client.UserID != recipientID {
			continue
		}
		select {
		case client.Send <- raw:
		default:
			slog.Warn("slow client dropped during whisper", "user", client.UserID)
			close(client.Send)
			delete(s.clients, client)
		}
	}
}

// sendStateSyncTo sends the full current VTT state to a single client.
func (s *Session) sendStateSyncTo(client *Client) {
	payload, err := json.Marshal(s.state)
	if err != nil {
		slog.Error("failed to marshal state sync", "err", err)
		return
	}
	client.sendMessage(Message{
		Type:       TypeStateSync,
		CampaignID: s.campaignID,
		Payload:    json.RawMessage(payload),
	})
}

// sendChatHistoryTo fetches the last N messages from DB and sends them to a single client.
func (s *Session) sendChatHistoryTo(client *Client) {
	msgs, err := s.store.LoadChatHistory(s.campaignID, chatHistoryLimit)
	if err != nil {
		slog.Error("failed to load chat history", "campaign", s.campaignID, "err", err)
		return
	}

	// Convert to payload type
	history := make([]ChatMessagePayload, 0, len(msgs))
	for _, m := range msgs {
		// Apply visibility rules to history:
		// Skip gm_only messages if client is not GM
		if m.GmOnly && !client.IsGM {
			continue
		}
		// Skip whispers not intended for this client
		if m.RecipientID != "" && m.UserID != client.UserID && m.RecipientID != client.UserID {
			continue
		}
		history = append(history, ChatMessagePayload{
			ID:          m.ID,
			CampaignID:  m.CampaignID,
			UserID:      m.UserID,
			Username:    m.Username,
			CharacterID: m.CharacterID,
			RecipientID: m.RecipientID,
			MessageType: m.MessageType,
			Content:     m.Content,
			GmOnly:      m.GmOnly,
			CreatedAt:   m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	payload, err := json.Marshal(ChatHistoryPayload{Messages: history})
	if err != nil {
		slog.Error("failed to marshal chat history", "err", err)
		return
	}

	client.sendMessage(Message{
		Type:       TypeChatHistory,
		CampaignID: s.campaignID,
		Payload:    json.RawMessage(payload),
	})
}

// sendError sends an error message to a specific client.
func (s *Session) sendError(client *Client, code, message string) {
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: message})
	client.sendMessage(Message{
		Type:       TypeError,
		CampaignID: s.campaignID,
		Payload:    json.RawMessage(payload),
	})
}

// close shuts down the session gracefully.
func (s *Session) close() {
	close(s.quit)
}

// clientCount returns the number of connected clients.
func (s *Session) clientCount() int {
	return len(s.clients)
}
