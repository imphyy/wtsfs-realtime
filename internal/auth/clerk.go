package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// Claims holds the verified identity extracted from a Clerk JWT.
type Claims struct {
	UserID string
	// Clerk puts the user ID in the "sub" claim.
	// Additional Clerk metadata (org, role) can be added here as needed.
}

// Verifier validates Clerk-issued JWTs using JWKS.
// It caches the key set and refreshes automatically.
type Verifier struct {
	jwksURL string
	cache   *jwk.Cache
	mu      sync.RWMutex
}

// NewVerifier creates a Verifier that fetches keys from the given JWKS URL.
// jwksURL example: "https://YOUR_CLERK_DOMAIN/.well-known/jwks.json"
func NewVerifier(ctx context.Context, jwksURL string) (*Verifier, error) {
	cache := jwk.NewCache(ctx)

	// Register with a 15-minute refresh interval
	if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return nil, fmt.Errorf("jwks register: %w", err)
	}

	// Pre-fetch on startup to catch bad URLs early
	if _, err := cache.Refresh(ctx, jwksURL); err != nil {
		return nil, fmt.Errorf("jwks initial fetch: %w", err)
	}

	slog.Info("JWKS loaded", "url", jwksURL)
	return &Verifier{jwksURL: jwksURL, cache: cache}, nil
}

// Verify parses and validates a Clerk JWT, returning the extracted claims.
func (v *Verifier) Verify(ctx context.Context, tokenStr string) (*Claims, error) {
	keySet, err := v.cache.Get(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}

	token, err := jwt.ParseString(tokenStr,
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt parse: %w", err)
	}

	sub := token.Subject()
	if sub == "" {
		return nil, errors.New("jwt missing sub claim")
	}

	return &Claims{UserID: sub}, nil
}

// ExtractToken pulls a Bearer token from the Authorization header,
// or falls back to the "token" query parameter (for WebSocket handshakes
// where custom headers aren't always supported).
func ExtractToken(r *http.Request) (string, error) {
	// Try Authorization header first
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		t := strings.TrimPrefix(auth, "Bearer ")
		if t != "" {
			return t, nil
		}
	}

	// Fall back to query param (WebSocket clients often can't set headers)
	if t := r.URL.Query().Get("token"); t != "" {
		return t, nil
	}

	return "", errors.New("no bearer token found in Authorization header or ?token= query param")
}
