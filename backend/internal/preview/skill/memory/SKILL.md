---
name: memory
description: Save a valuable fragment to the user's personal memory (a durable, searchable note store). Use when the user says "remember this", "save to memory", "note this down", or when a decision/insight/idea worth keeping emerges — and to distill a long session into a session summary plus atomic facts.
---

# brigade memory

This session runs inside brigade. The user has a **personal memory** — markdown notes in a
private git repo, searchable in the brigade dashboard, that **survive session deletion**.
You post notes to it via a ConnectRPC call.

Memory has two **layers** — pick the right one per note:

- **`semantic`** (default) — one atomic, durable fact/idea, reusable across sessions. Pick a
  `type`: `idea | decision | insight | todo | question | reference`.
- **`episodic`** — a summary of *this session*: what was requested, done, learned, and what's
  next. One per session (or per major milestone).

## Save a note

Plain `POST` to `brigade.v1.AgentBridgeService/CreateMemoryNote` — no client library needed:

```sh
curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/CreateMemoryNote" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"title\": \"Graph vs Kanban\", \"body\": \"Граф/канбан — способ отрисовки, а не хранения.\", \"type\": \"insight\", \"layer\": \"semantic\", \"tags\": [\"brigade\", \"memory\"]}"
```

`body` is markdown. `layer` defaults to `semantic` if omitted. The response is
`{"id": "...", "commitSha": "..."}` — `commitSha` proves it's durably pushed. Tell the user
it's saved.

## Distill a session into layered memory

When asked to "save this session to memory" (or the session got long and valuable), write
**both layers**:

1. **One `episodic` note** — the session summary. Set `layer: "episodic"`, `type: "summary"`,
   and structure the `body` in markdown:

   ```
   **Запрос:** …
   **Сделано:** …
   **Узнал:** …
   **Дальше:** …
   ```

2. **Several `semantic` notes** — the durable atomic facts worth keeping (one idea each), with
   a fitting `type` and `tags`, `layer: "semantic"`.

POST them one by one with the call above. Report how many notes you saved and the session
summary id.

Notes:
- Notes survive even if this session is later deleted (`sessionId` is kept only as provenance).
- If the call returns a `failed_precondition` error, the user hasn't configured their memory
  repository yet — tell them to set it in Settings → Память, don't retry.
