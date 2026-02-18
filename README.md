# wtsfs-realtime

Real-time WebSocket server for the **When The Stars Fall Silent** VTT.

Handles in-session state (token positions, fog of war, map) in memory with periodic persistence to Postgres. Auth is validated via Clerk JWKS.

## Architecture

```
Browser (Next.js)
   ↕ WebSocket (/ws?campaign=<id>&token=<clerk_jwt>)
wtsfs-realtime (Go, Fly.io)
   ├── Hub — manages all campaign sessions
   ├── Session — per-campaign goroutine (owns state, no locks needed)
   ├── Client — per-connection read/write pumps
   ├── Auth — Clerk JWT validation via JWKS
   └── Persist — 5s flush to Postgres vtt_sessions table
```

## Message Protocol

All messages are JSON with this envelope:

```json
{
  "type": "token.move",
  "campaign_id": "<uuid>",
  "user_id": "<clerk-user-id>",
  "payload": { ... }
}
```

### Client → Server messages

| Type | Payload | GM only? |
|------|---------|----------|
| `token.add` | `{ token_id, character_id?, name, image_url?, x, y, width, height, visible }` | No |
| `token.move` | `{ token_id, x, y }` | No |
| `token.remove` | `{ token_id }` | No |
| `token.update` | `{ token_id, name?, image_url?, width?, height?, visible? }` | visible field: GM only |
| `fog.update` | `{ zones: [{x, y, w, h, visible}] }` | Yes |
| `map.set` | `{ map_id, image_url, grid_size, width, height }` | Yes |
| `ping` | (empty) | No |

### Server → Client messages

| Type | Description |
|------|-------------|
| `state.sync` | Full session state sent on connect |
| `token.add/move/remove/update` | Broadcast of any client's changes |
| `fog.update` | Broadcast fog state |
| `map.set` | Broadcast map change |
| `pong` | Response to ping |
| `error` | `{ code, message }` |

## Local Development

```bash
# Copy env
cp .env.example .env

# Edit .env with your values
# DATABASE_URL=postgresql://...
# CLERK_JWKS_URL=https://your-clerk-domain.clerk.accounts.dev/.well-known/jwks.json

# Run
go run ./cmd/server

# Server starts on :8080
# Health check: http://localhost:8080/health
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `DATABASE_URL` | ✅ | Neon Postgres connection string (same as Next.js app) |
| `CLERK_JWKS_URL` | ✅ | `https://<your-clerk-domain>/.well-known/jwks.json` |
| `PORT` | No | HTTP port (default: `8080`) |

### Finding your Clerk JWKS URL

In your Clerk dashboard → **API Keys** → **Advanced** → copy the Frontend API URL.
Your JWKS URL is: `https://<frontend-api-url>/.well-known/jwks.json`

## Deployment (Fly.io)

```bash
# Install flyctl if needed
brew install flyctl

# Login
flyctl auth login

# Create the app (first time only)
flyctl apps create wtsfs-realtime

# Set secrets
flyctl secrets set \
  DATABASE_URL="postgresql://..." \
  CLERK_JWKS_URL="https://your-clerk-domain.clerk.accounts.dev/.well-known/jwks.json"

# Deploy
flyctl deploy

# Check logs
flyctl logs
```

The `fly.toml` is pre-configured with:
- Region: `lhr` (London) — change to match your Neon DB region
- `auto_stop_machines = false` — required: VTT sessions live in memory
- 1 machine minimum — no cold starts mid-session

## Connecting from Next.js

```typescript
// hooks/useVTT.ts
import { useAuth } from '@clerk/nextjs'
import { useEffect, useRef } from 'react'

const WS_URL = process.env.NEXT_PUBLIC_VTT_WS_URL // e.g. wss://wtsfs-realtime.fly.dev

export function useVTT(campaignId: string) {
  const { getToken } = useAuth()
  const ws = useRef<WebSocket | null>(null)

  useEffect(() => {
    let socket: WebSocket

    async function connect() {
      const token = await getToken()
      socket = new WebSocket(`${WS_URL}/ws?campaign=${campaignId}&token=${token}`)

      socket.onmessage = (e) => {
        const msg = JSON.parse(e.data)
        // dispatch to your canvas state manager
      }

      socket.onclose = () => {
        // Reconnect after 2s
        setTimeout(connect, 2000)
      }
    }

    connect()
    return () => socket?.close()
  }, [campaignId])

  const send = (type: string, payload: unknown) => {
    ws.current?.send(JSON.stringify({ type, payload }))
  }

  return { send }
}
```

Add to your Next.js `.env.local`:
```
NEXT_PUBLIC_VTT_WS_URL=wss://wtsfs-realtime.fly.dev
```

## Database

The service creates and manages one table in the shared Neon database:

```sql
CREATE TABLE vtt_sessions (
  campaign_id  TEXT PRIMARY KEY,
  state        JSONB        NOT NULL DEFAULT '{}',
  updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
```

This is created automatically on first startup. It only stores active VTT map/token/fog state — not character data (that stays in the Next.js tables).
