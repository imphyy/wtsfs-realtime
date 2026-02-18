package persist

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/lib/pq"
)

// Store handles persistence of VTT session state to Postgres.
// It uses a dedicated vtt_sessions table rather than touching the
// existing application tables directly.
type Store struct {
	db *sql.DB
}

// New opens a Postgres connection and ensures the vtt_sessions table exists.
func New(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	slog.Info("postgres connected")
	return s, nil
}

// migrate ensures the vtt_sessions table exists.
// Uses IF NOT EXISTS so it's safe to call on every startup.
func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS vtt_sessions (
			campaign_id  TEXT PRIMARY KEY,
			state        JSONB        NOT NULL DEFAULT '{}',
			updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
		)
	`)
	return err
}

// SessionState mirrors the hub.SessionState but is defined here to avoid
// an import cycle. We use json.RawMessage to stay agnostic of the hub types.
type SessionState = json.RawMessage

// Load retrieves the last saved state for a campaign.
// Returns an empty state (not an error) if no row exists yet.
func (s *Store) Load(campaignID string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var raw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT state FROM vtt_sessions WHERE campaign_id = $1`,
		campaignID,
	).Scan(&raw)

	if err == sql.ErrNoRows {
		return nil, nil // caller handles nil as "start fresh"
	}
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	return json.RawMessage(raw), nil
}

// Flush writes the current session state to Postgres (upsert).
// Called every 5 seconds by the session ticker, and on session close.
func (s *Store) Flush(campaignID string, state any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO vtt_sessions (campaign_id, state, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (campaign_id) DO UPDATE
		  SET state = EXCLUDED.state,
		      updated_at = EXCLUDED.updated_at
	`, campaignID, raw)

	if err != nil {
		return fmt.Errorf("flush state: %w", err)
	}

	return nil
}

// Delete removes the persisted state for a campaign (e.g. when a campaign ends).
func (s *Store) Delete(campaignID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM vtt_sessions WHERE campaign_id = $1`,
		campaignID,
	)
	return err
}

// Close closes the database connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}
