---
name: note
description: Save a valuable fragment to the user's personal memory (a durable, searchable note store organized into topics). Use when the user says "remember this", "save to memory", "save a note", "note this down", "запиши в память", "сохрани заметку", or when a decision/insight/idea worth keeping emerges — and to distill a long session into a session summary plus atomic facts.
---

# brigade note

This session runs inside brigade. The user has a **personal memory** — markdown notes in a
private git repo, searchable in the brigade dashboard, that **survive session deletion**.
Notes are organized into **topics** (notebooks). You post notes to it via a ConnectRPC call.

Each note has:

- **`topic`** — the owning topic, by **human name** (e.g. `"DIY"`, `"Работа"`). Created if it
  doesn't exist yet; matched to an existing topic by name. **Omit it → the note lands in the
  default «Общее» topic.** When the user names a topic ("создай тему DIY и заметку в ней",
  "in the Work topic"), you MUST pass `topic` — otherwise the note goes to «Общее».
- **`tags`** — free labels for search (e.g. `["bosch", "аккумуляторы"]`). Tags are NOT topics:
  putting `DIY` only in tags does NOT place the note in the DIY topic. Use `topic` for that.
- **`layer`** — `semantic` (default) one atomic fact, or `episodic` a session summary.
- **`type`** — `idea | decision | insight | todo | question | reference` (semantic notes).

## Save a note

Plain `POST` to `brigade.v1.AgentBridgeService/CreateMemoryNote` — no client library needed:

```sh
curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/CreateMemoryNote" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"topic\": \"DIY\", \"title\": \"Аккумуляторы для Bosch GKS 18V-57\", \"body\": \"Совместимы GBA 18V / ProCORE18V (BAT609/618/619).\", \"type\": \"reference\", \"tags\": [\"bosch\", \"аккумуляторы\"]}"
```

`body` is markdown. `topic` is the topic name (omit → «Общее»). `layer` defaults to `semantic`.
The response is `{"id": "...", "commitSha": "..."}` — `commitSha` proves it's durably pushed.
Tell the user it's saved and in which topic.

## Distill a session into layered memory

When asked to "save this session to memory" (or the session got long and valuable), write
**both layers** (pick a fitting `topic` for the session's subject):

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

POST them one by one with the call above. Report how many notes you saved, into which topic,
and the session summary id.

Notes:
- Notes survive even if this session is later deleted (`sessionId` is kept only as provenance).
- If the call returns a `failed_precondition` error, the user hasn't configured their memory
  repository yet — tell them to set it in Settings → Память, don't retry.
