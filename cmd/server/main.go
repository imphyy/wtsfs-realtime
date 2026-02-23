package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/williamhunt/wtsfs-realtime/internal/auth"
	"github.com/williamhunt/wtsfs-realtime/internal/hub"
	"github.com/williamhunt/wtsfs-realtime/internal/persist"
	"github.com/williamhunt/wtsfs-realtime/internal/ws"
)

func main() {
	// Load .env file if present (dev only — production uses real env vars)
	if err := godotenv.Load(); err == nil {
		fmt.Println("loaded .env file")
	}

	// Structured JSON logging (Fly.io picks this up nicely)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Persistence ---
	store, err := persist.New(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// --- Auth ---
	verifier, err := auth.NewVerifier(ctx, cfg.ClerkJWKSURL)
	if err != nil {
		slog.Error("failed to initialise Clerk verifier", "err", err)
		os.Exit(1)
	}

	// --- Hub ---
	h := hub.New(store)

	// isGMFunc: checks whether a user is the GM of a campaign.
	// Queries the existing Next.js campaigns table directly.
	isGMFunc := makeIsGMFunc(cfg.DatabaseURL)

	// --- Routes ---
	mux := http.NewServeMux()
	mux.Handle("/ws", ws.NewHandler(h, verifier, isGMFunc))
	mux.HandleFunc("/health", ws.HealthHandler(h))
	mux.HandleFunc("/internal/broadcast", ws.InternalHandler(h, cfg.InternalAPISecret))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wtsfs-realtime", http.StatusOK)
	})

	// --- Idle session cleanup (every 5 minutes) ---
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				h.CloseIdleSessions()
			case <-ctx.Done():
				return
			}
		}
	}()

	// --- Server ---
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // WebSocket connections are long-lived
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down...")
		cancel()

		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Error("shutdown error", "err", err)
		}
	}()

	slog.Info("server starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// config holds all runtime configuration loaded from environment variables.
type config struct {
	Port              string
	DatabaseURL       string
	ClerkJWKSURL      string
	InternalAPISecret string
}

func loadConfig() (*config, error) {
	cfg := &config{
		Port:              getEnv("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		ClerkJWKSURL:      os.Getenv("CLERK_JWKS_URL"),
		InternalAPISecret: os.Getenv("INTERNAL_API_SECRET"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.ClerkJWKSURL == "" {
		return nil, fmt.Errorf("CLERK_JWKS_URL is required (e.g. https://your-clerk-domain.clerk.accounts.dev/.well-known/jwks.json)")
	}
	if cfg.InternalAPISecret == "" {
		return nil, fmt.Errorf("INTERNAL_API_SECRET is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// makeIsGMFunc returns a function that queries the campaigns table to check
// if a given Clerk user ID is the GM (gm_id) of a campaign.
// This reuses the same Postgres connection string as the Next.js app.
func makeIsGMFunc(dsn string) func(userID, campaignID string) bool {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		slog.Error("failed to open db for GM checks", "err", err)
		// Fall back to no-one being GM (safe default)
		return func(_, _ string) bool { return false }
	}

	return func(userID, campaignID string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var isGM bool
		err := db.QueryRowContext(ctx,
			`SELECT gm_id = $1 FROM campaigns WHERE id = $2`,
			userID, campaignID,
		).Scan(&isGM)

		if err != nil {
			// If we can't verify, deny GM access
			slog.Warn("GM check failed", "user", userID, "campaign", campaignID, "err", err)
			return false
		}
		return isGM
	}
}
