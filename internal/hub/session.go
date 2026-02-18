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
			// Send full current state to the new client
			s.sendStateSyncTo(client)

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
		s.broadcast(msg, nil) // broadcast to ALL including sender so they can confirm

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
		// Partial update — only non-nil fields
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
			// Only GMs can change visibility
			if cm.client.IsGM {
				token.Visible = *p.Visible
			} else {
				s.sendError(cm.client, "forbidden", "only GMs can change token visibility")
				return
			}
		}
		s.broadcast(msg, nil)

	case TypeFogUpdate:
		// GM only
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
		// GM only
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
		// Clear tokens and fog when map changes
		s.state.Tokens = make(map[string]*Token)
		s.state.FogZones = nil
		s.broadcast(msg, nil)

	case TypePing:
		cm.client.sendMessage(Message{Type: TypePong, CampaignID: s.campaignID})

	default:
		slog.Warn("unknown message type", "type", msg.Type, "user", cm.client.UserID)
	}
}

// broadcast sends a message to all clients, optionally skipping one (e.g. the sender).
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
			// Slow client — close it
			slog.Warn("slow client dropped", "user", client.UserID)
			close(client.Send)
			delete(s.clients, client)
		}
	}
}

// sendStateSyncTo sends the full current session state to a single client.
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
