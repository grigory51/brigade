---
name: brigade-preview
description: Expose a dev server running in this session at a public preview URL. Use when the user asks to run, preview, or share the project's web app/dev server.
---

# brigade preview

This session runs inside brigade. Any HTTP server you start here can be reached
from the user's browser at a deterministic preview URL — no port forwarding needed.

## How to expose a dev server

1. Start the server on any free port. **If this session runs in Docker, bind to
   `0.0.0.0`, not localhost** (the proxy connects over the container network):
   - vite: `vite --host 0.0.0.0`; also allow the preview host via
     `server.allowedHosts: true` (or list the host) in vite config;
   - next: `next dev -H 0.0.0.0`;
   - python: `python3 -m http.server 8000 --bind 0.0.0.0`.

2. Build the preview URL: take `$BRIGADE_PREVIEW_URL_TEMPLATE` and replace `{port}`
   with the actual port. The template is already the full, correct URL for this
   deployment (subdomain or single-host cookie form) — just substitute the port,
   do not assemble it yourself. Examples:
   - `http://abc-{port}.localhost:10000` → `http://abc-3000.localhost:10000`
   - `https://preview.example.com/?id=abc-{port}` → `https://preview.example.com/?id=abc-3000`

3. Register the port so the link shows up in the brigade UI. This calls the
   `brigade.v1.AgentBridgeService/RegisterPreview` ConnectRPC method — plain
   `POST` with a JSON body, no client library needed:

   ```sh
   curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/RegisterPreview" \
     -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
     -H "Content-Type: application/json" \
     -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"port\": 3000, \"name\": \"vite\"}"
   ```

   The response contains the final URL: `{"url": "..."}`.

4. Tell the user the preview URL.

Notes:
- The URL works as soon as the server is listening; registration only adds the
  link to the UI.
- Re-register after restarting brigade if the link disappeared from the UI.
