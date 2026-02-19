package persist

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ChatMessage mirrors the chat_messages table row.
type ChatMessage struct {
	ID          string          `json:"id"`
	CampaignID  string          `json:"campaign_id"`
	UserID      string          `json:"user_id"`
	Username    string          `json:"username,omitempty"`
	CharacterID string          `json:"character_id,omitempty"`
	RecipientID string          `json:"recipient_id,omitempty"`
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`
	GmOnly      bool            `json:"gm_only"`
	CreatedAt   time.Time       `json:"created_at"`
}

// SaveChatMessage inserts a new chat message into the chat_messages table
// and returns the saved message (with generated ID and timestamp).
func (s *Store) SaveChatMessage(userID, campaignID, messageType string, content json.RawMessage, gmOnly bool, recipientID, characterID string) (*ChatMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		id        string
		createdAt time.Time
		username  string
	)

	// Insert message and fetch username in one query
	err := s.db.QueryRowContext(ctx, `
		WITH inserted AS (
			INSERT INTO chat_messages (campaign_id, user_id, character_id, recipient_id, message_type, content, gm_only)
			VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7)
			RETURNING id, created_at
		)
		SELECT i.id, i.created_at, COALESCE(u.username, '') 
		FROM inserted i
		LEFT JOIN users u ON u.id = $2
	`, campaignID, userID, characterID, recipientID, messageType, []byte(content), gmOnly).
		Scan(&id, &createdAt, &username)

	if err != nil {
		return nil, fmt.Errorf("save chat message: %w", err)
	}

	return &ChatMessage{
		ID:          id,
		CampaignID:  campaignID,
		UserID:      userID,
		Username:    username,
		CharacterID: characterID,
		RecipientID: recipientID,
		MessageType: messageType,
		Content:     content,
		GmOnly:      gmOnly,
		CreatedAt:   createdAt,
	}, nil
}

// LoadChatHistory returns the last N messages for a campaign, oldest first.
func (s *Store) LoadChatHistory(campaignID string, limit int) ([]ChatMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT 
			cm.id,
			cm.campaign_id,
			cm.user_id,
			COALESCE(u.username, '') AS username,
			COALESCE(cm.character_id::text, '') AS character_id,
			COALESCE(cm.recipient_id, '') AS recipient_id,
			cm.message_type,
			cm.content,
			cm.gm_only,
			cm.created_at
		FROM (
			SELECT * FROM chat_messages
			WHERE campaign_id = $1
			ORDER BY created_at DESC
			LIMIT $2
		) cm
		LEFT JOIN users u ON u.id = cm.user_id
		ORDER BY cm.created_at ASC
	`, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("load chat history: %w", err)
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		var content []byte
		if err := rows.Scan(
			&msg.ID,
			&msg.CampaignID,
			&msg.UserID,
			&msg.Username,
			&msg.CharacterID,
			&msg.RecipientID,
			&msg.MessageType,
			&content,
			&msg.GmOnly,
			&msg.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan chat message: %w", err)
		}
		msg.Content = json.RawMessage(content)
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}
