package hub

import "encoding/json"

// Message is the wire format for all WebSocket messages.
// The Payload field is left as raw JSON and decoded per-type by the session.
type Message struct {
	Type      string          `json:"type"`
	CampaignID string         `json:"campaign_id"`
	UserID    string          `json:"user_id"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Message types sent by clients → server → other clients
const (
	TypeTokenAdd    = "token.add"
	TypeTokenMove   = "token.move"
	TypeTokenRemove = "token.remove"
	TypeTokenUpdate = "token.update" // name, image, size changes
	TypeFogUpdate   = "fog.update"
	TypeMapSet      = "map.set"
	TypePing        = "ping"
	TypePong        = "pong"
	TypeStateSync   = "state.sync"  // server → client on connect: full current state
	TypeError       = "error"
)

// --- Payload types ---

type TokenAddPayload struct {
	TokenID     string  `json:"token_id"`
	CharacterID string  `json:"character_id,omitempty"`
	Name        string  `json:"name"`
	ImageURL    string  `json:"image_url,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Visible     bool    `json:"visible"`  // visible to players (GM can hide)
}

type TokenMovePayload struct {
	TokenID string  `json:"token_id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

type TokenRemovePayload struct {
	TokenID string `json:"token_id"`
}

type TokenUpdatePayload struct {
	TokenID  string  `json:"token_id"`
	Name     *string `json:"name,omitempty"`
	ImageURL *string `json:"image_url,omitempty"`
	Width    *float64 `json:"width,omitempty"`
	Height   *float64 `json:"height,omitempty"`
	Visible  *bool   `json:"visible,omitempty"`
}

type FogZone struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"w"`
	Height  float64 `json:"h"`
	Visible bool    `json:"visible"`
}

type FogUpdatePayload struct {
	Zones []FogZone `json:"zones"`
}

type MapSetPayload struct {
	MapID    string  `json:"map_id"`
	ImageURL string  `json:"image_url"`
	GridSize float64 `json:"grid_size"` // pixels per grid cell
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// --- Session state (persisted to Postgres) ---

type Token struct {
	TokenID     string  `json:"token_id"`
	CharacterID string  `json:"character_id,omitempty"`
	Name        string  `json:"name"`
	ImageURL    string  `json:"image_url,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Visible     bool    `json:"visible"`
}

type SessionState struct {
	CampaignID string             `json:"campaign_id"`
	MapID      string             `json:"map_id,omitempty"`
	MapURL     string             `json:"map_url,omitempty"`
	GridSize   float64            `json:"grid_size"`
	MapWidth   float64            `json:"map_width"`
	MapHeight  float64            `json:"map_height"`
	Tokens     map[string]*Token  `json:"tokens"`  // tokenId → Token
	FogZones   []FogZone          `json:"fog_zones"`
}
