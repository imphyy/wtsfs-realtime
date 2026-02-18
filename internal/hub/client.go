package hub

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 64 * 1024 // 64 KB
)

// Client represents a single WebSocket connection.
// Fields are exported so the ws package can construct one before handing it to the hub.
type Client struct {
	UserID     string
	CampaignID string
	IsGM       bool
	Conn       *websocket.Conn
	Send       chan []byte // buffered channel of outbound messages
	session    *Session
}

// ClientMessage bundles an inbound message with its sender.
type ClientMessage struct {
	client  *Client
	message Message
}

// ReadPump pumps messages from the WebSocket connection into the session's inbound channel.
// One goroutine per client. Closes the client on error or disconnect.
func (c *Client) ReadPump() {
	defer func() {
		c.session.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("websocket read error", "user", c.UserID, "err", err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			slog.Warn("invalid message from client", "user", c.UserID, "err", err)
			continue
		}

		// Stamp with authenticated identity — don't trust client-supplied user_id
		msg.UserID = c.UserID
		msg.CampaignID = c.CampaignID

		c.session.in <- ClientMessage{client: c, message: msg}
	}
}

// WritePump pumps messages from the Send channel to the WebSocket connection.
// One goroutine per client.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Session closed the channel
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				slog.Warn("websocket write error", "user", c.UserID, "err", err)
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendMessage sends a message directly to this client (internal use by session).
func (c *Client) sendMessage(msg Message) {
	raw, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal message", "err", err)
		return
	}
	select {
	case c.Send <- raw:
	default:
		// Client send buffer full — drop and close
		slog.Warn("client send buffer full, dropping", "user", c.UserID)
		close(c.Send)
	}
}
