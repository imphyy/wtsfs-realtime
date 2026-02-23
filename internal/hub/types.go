package hub

import "encoding/json"

// Message is the wire format for all WebSocket messages.
// The Payload field is left as raw JSON and decoded per-type by the session.
type Message struct {
	Type       string          `json:"type"`
	CampaignID string          `json:"campaign_id"`
	UserID     string          `json:"user_id"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Message types sent by clients → server → other clients
const (
	// VTT types
	TypeTokenAdd    = "token.add"
	TypeTokenMove   = "token.move"
	TypeTokenRemove = "token.remove"
	TypeTokenUpdate = "token.update" // name, image, size changes
	TypeFogUpdate   = "fog.update"
	TypeMapSet      = "map.set"
	TypeMapSwitch   = "map.switch"  // GM switches to a saved map
	TypeMapDelete   = "map.delete"  // GM removes a map from the library
	TypeMapRename   = "map.rename"  // GM renames a map in the library
	TypeMapList        = "map.list"        // server → clients: map library contents
	TypeSceneDarkness  = "scene.darkness"  // GM sets ambient darkness level (0-1)
	TypePing           = "ping"
	TypePong        = "pong"
	TypeStateSync   = "state.sync" // server → client on connect: full current state
	TypeError       = "error"

	// Chat types
	TypeChatSend    = "chat.send"    // client → server: send a new message
	TypeChatMessage = "chat.message" // server → clients: a new message broadcast
	TypeChatHistory = "chat.history" // server → client on connect: last 100 messages
)

// --- Chat payload types ---

// ChatSendPayload is sent by a client to post a new chat message.
type ChatSendPayload struct {
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`
	GmOnly      bool            `json:"gm_only"`
	RecipientID string          `json:"recipient_id,omitempty"`
	CharacterID string          `json:"character_id,omitempty"`
}

// ChatMessagePayload is the full message broadcast to all clients.
type ChatMessagePayload struct {
	ID          string          `json:"id"`
	CampaignID  string          `json:"campaign_id"`
	UserID      string          `json:"user_id"`
	Username    string          `json:"username,omitempty"`
	CharacterID string          `json:"character_id,omitempty"`
	RecipientID string          `json:"recipient_id,omitempty"`
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`
	GmOnly      bool            `json:"gm_only"`
	CreatedAt   string          `json:"created_at"`
}

// ChatHistoryPayload is sent to a client on connect with recent messages.
type ChatHistoryPayload struct {
	Messages []ChatMessagePayload `json:"messages"`
}

// --- VTT payload types ---

type TokenAddPayload struct {
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

type TokenMovePayload struct {
	TokenID string  `json:"token_id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

type TokenRemovePayload struct {
	TokenID string `json:"token_id"`
}

type TokenUpdatePayload struct {
	TokenID  string   `json:"token_id"`
	Name     *string  `json:"name,omitempty"`
	ImageURL *string  `json:"image_url,omitempty"`
	Width    *float64 `json:"width,omitempty"`
	Height   *float64 `json:"height,omitempty"`
	Visible  *bool    `json:"visible,omitempty"`
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
	GridSize float64 `json:"grid_size"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Name     string  `json:"name,omitempty"` // human-friendly map name
	// Optional dd2vtt metadata (sent when importing a .dd2vtt file)
	MapMetadata *MapMetadata `json:"map_metadata,omitempty"`
}

type MapSwitchPayload struct {
	MapID string `json:"map_id"`
}

type MapDeletePayload struct {
	MapID string `json:"map_id"`
}

type MapRenamePayload struct {
	MapID   string `json:"map_id"`
	NewName string `json:"new_name"`
}

type SceneDarknessPayload struct {
	DarknessLevel float64 `json:"darkness_level"`
}

// --- DD2VTT / Universal VTT map metadata ---

type MapPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type MapPortal struct {
	Position    MapPoint    `json:"position"`
	Bounds      [2]MapPoint `json:"bounds"`
	Rotation    float64     `json:"rotation"`
	Closed      bool        `json:"closed"`
	Freestanding bool       `json:"freestanding"`
}

type MapLight struct {
	Position  MapPoint `json:"position"`
	Range     float64  `json:"range"`
	Intensity float64  `json:"intensity"`
	Color     string   `json:"color"`
	Shadows   bool     `json:"shadows"`
}

type MapEnvironment struct {
	BakedLighting bool   `json:"baked_lighting"`
	AmbientLight  string `json:"ambient_light"`
}

// MapMetadata stores wall, light, and portal data from dd2vtt imports.
// All coordinates are in grid squares (not pixels).
type MapMetadata struct {
	Walls       [][]MapPoint   `json:"walls,omitempty"`
	ObjectWalls [][]MapPoint   `json:"object_walls,omitempty"`
	Portals     []MapPortal    `json:"portals,omitempty"`
	Lights      []MapLight     `json:"lights,omitempty"`
	Environment MapEnvironment `json:"environment"`
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
	CampaignID    string               `json:"campaign_id"`
	MapID         string               `json:"map_id,omitempty"`
	MapURL        string               `json:"map_url,omitempty"`
	GridSize      float64              `json:"grid_size"`
	MapWidth      float64              `json:"map_width"`
	MapHeight     float64              `json:"map_height"`
	DarknessLevel float64              `json:"darkness_level"`
	Tokens        map[string]*Token    `json:"tokens"`
	FogZones      []FogZone            `json:"fog_zones"`
	MapMetadata   *MapMetadata         `json:"map_metadata,omitempty"`
	SavedMaps     map[string]*SavedMap `json:"saved_maps,omitempty"`
}

// --- Map library types ---

// SavedMap holds the full state snapshot for a single map in the library.
type SavedMap struct {
	Name        string            `json:"name"`
	ImageURL    string            `json:"image_url"`
	GridSize    float64           `json:"grid_size"`
	Width       float64           `json:"width"`
	Height      float64           `json:"height"`
	Tokens      map[string]*Token `json:"tokens"`
	FogZones    []FogZone         `json:"fog_zones"`
	MapMetadata *MapMetadata      `json:"map_metadata,omitempty"`
}

// MapListEntry is a lightweight summary of a saved map (no tokens/fog).
type MapListEntry struct {
	MapID    string `json:"map_id"`
	Name     string `json:"name"`
	ImageURL string `json:"image_url"`
}

// MapListPayload is broadcast to clients with the current map library.
type MapListPayload struct {
	Maps        []MapListEntry `json:"maps"`
	ActiveMapID string         `json:"active_map_id"`
}
