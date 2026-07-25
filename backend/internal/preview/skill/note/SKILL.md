---
name: note
description: Save a valuable fragment to the user's personal memory (a durable, searchable note store organized into topics). Use when the user runs /note, says "remember this", "save to memory", "save a note", "note this down", "запиши в память", "сохрани заметку", or when a decision/insight/idea worth keeping emerges — and to distill a long session into a session summary plus atomic facts.
---

# brigade note

This session runs inside brigade. The user has a **personal memory** — markdown notes in a
private git repo, searchable in the brigade dashboard, that **survive session deletion**.
Notes are organized into **topics** (notebooks), each with optional **subtopics**.

Each note has:

- **`topic`** — the owning topic, by **human name** (e.g. `"DIY"`, `"Работа"`). Created if it
  doesn't exist; matched to an existing topic by name. Omit → the default «Общее» topic.
- **`sub`** — subtopic inside the topic (e.g. `"Аккумуляторы"`). Omit → «Общее».
- **`tags`** — free labels for search. Tags are NOT topics.
- **`layer`** — `semantic` (default) one atomic fact, or `episodic` a session summary.
- **`type`** — `idea | decision | insight | todo | question | reference`.

## The user's request carries context

When the user runs `/note`, the message often comes with an appended **`Контекст:`** block —
quoted fragments the user picked from the chat, and/or attached file paths. **That block is the
raw material for the note.** Combine it with whatever the user typed after `/note` to form the
draft. If the user named a topic/subtopic/type in their text ("in DIY, reference", "тема Работа,
решение"), use those as defaults; otherwise infer sensible ones.

## /note → show a save card (brigade saves it, not you)

Call the **`save_note`** tool with a draft, then **STOP**. brigade renders a native, editable
card in the chat; the user tweaks the fields and clicks Сохранить, and **brigade saves the note
directly** through its own API. You do **NOT** save it: do not call any API/curl, do not wait for
a follow-up message, do not parse any action. Your only job is a good draft.

Fill the tool arguments from the `Контекст:` block + what the user typed:

- `title` — one-line summary.
- `body` — the note text (markdown), distilled from the quoted fragments and the user's words.
- `topic` — topic name **if the user implied one** (e.g. `"DIY"`), else leave empty (→ «Общее»).
- `sub` — subtopic name if implied (e.g. `"3D"`), else empty.
- `type` — a single string, one of `idea|decision|insight|todo|question|reference`.
- `tags` — optional search labels.

Example: `save_note {"title":"PLA — филамент для 3D-печати","body":"**PLA** — биопластик из кукурузы…","topic":"DIY","sub":"3D","type":"reference","tags":["3d-printing"]}`.

Make **exactly one** `save_note` call and **nothing else**: no other tool, no text before or
after — not even "карточка добавлена". The card is self-explanatory and the user acts on it. Any
extra output is noise. Don't invent a topic the user didn't imply — leave it empty, the user picks.

## Distill a session into layered memory

When asked to "save this session to memory" (or the session got long and valuable), don't use
the card — save the notes directly with a plain POST (no card, they're many). Both layers, into
a fitting `topic`:

```sh
curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/CreateMemoryNote" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"topic\": \"DIY\", \"sub\": \"3D\", \"title\": \"…\", \"body\": \"…\", \"type\": \"reference\", \"tags\": []}"
```

1. **One `episodic` note** — the session summary. `layer: "episodic"`, `type: "summary"`, body:

   ```
   **Запрос:** …
   **Сделано:** …
   **Узнал:** …
   **Дальше:** …
   ```

2. **Several `semantic` notes** — durable atomic facts (one idea each), fitting `type`/`tags`,
   `layer: "semantic"`.

Report how many notes you saved, into which topic, and the session summary id.

## Notes

- Notes survive even if this session is later deleted (`sessionId` is kept only as provenance).
- If the call returns a `failed_precondition` error, the user hasn't configured their memory
  repository yet — tell them to set it in Settings → Память, don't retry.
