---
name: memory
description: Save a valuable fragment to the user's personal memory (a durable, searchable note store). Use when the user says "remember this", "save to memory", "note this down", or when a decision/insight/idea worth keeping emerges — and to distill a long session into atomic notes.
---

# brigade memory

This session runs inside brigade. The user has a **personal memory** — atomic markdown
notes in a private git repo, searchable in the brigade dashboard, that **survive session
deletion**. You post notes to it via a ConnectRPC call.

Store one idea per note (atomic). Pick a `type`:
`idea | decision | insight | todo | question | reference`.

## Save a note

Plain `POST` to the `brigade.v1.AgentBridgeService/CreateMemoryNote` method — no client
library needed:

```sh
curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/CreateMemoryNote" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"title\": \"Graph vs Kanban\", \"body\": \"Граф/канбан — способ отрисовки, а не хранения.\", \"type\": \"insight\", \"tags\": [\"brigade\", \"memory\"]}"
```

`body` is markdown. The response is `{"id": "...", "commitSha": "..."}` — the `commitSha`
proves the note is durably pushed. Tell the user it's saved.

## Distill a session into notes

When asked to "save this session to memory" (or the session got long and valuable):
split the conversation into **several atomic notes** — one idea each — and POST them one
by one with the call above, choosing a fitting `type` and `tags` per note. Report how many
notes you saved.

Notes:
- The note survives even if this session is later deleted (`sessionId` is kept only as
  provenance).
- If the call returns a `failed_precondition` error, personal memory is not configured on
  this brigade instance — tell the user instead of retrying.
