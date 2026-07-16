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

## /note → show an editable card, then save on confirm

Do **not** save silently. Render a review card in the chat and save **only** when the user
clicks Сохранить. This is a two-step tool flow:

**Step 1 — draw the card** with the `render_ui` tool. Prefill `dataModel` from the draft
(title, body, topic, sub, type). Use exactly this shape:

```json
{
  "components": [
    {"id":"root","component":"Card","child":"col"},
    {"id":"col","component":"Column","children":["h","f_title","f_body","f_topic","f_sub","f_type","save"]},
    {"id":"h","component":"Text","text":"Добавить в память","variant":"h4"},
    {"id":"f_title","component":"TextField","label":"Заголовок","value":{"path":"/title"}},
    {"id":"f_body","component":"TextField","label":"Текст заметки","variant":"longText","value":{"path":"/body"}},
    {"id":"f_topic","component":"TextField","label":"Тема","value":{"path":"/topic"}},
    {"id":"f_sub","component":"TextField","label":"Подтема","value":{"path":"/sub"}},
    {"id":"f_type","component":"ChoicePicker","variant":"mutuallyExclusive","displayStyle":"chips","value":{"path":"/type"},"options":[{"value":"idea","label":"идея"},{"value":"decision","label":"решение"},{"value":"insight","label":"инсайт"},{"value":"todo","label":"todo"},{"value":"question","label":"вопрос"},{"value":"reference","label":"справка"}]},
    {"id":"save_label","component":"Text","text":"Сохранить"},
    {"id":"save","component":"Button","child":"save_label","action":{"event":{"name":"save_note","context":{"title":{"path":"/title"},"body":{"path":"/body"},"topic":{"path":"/topic"},"sub":{"path":"/sub"},"type":{"path":"/type"}}}}}
  ],
  "dataModel": {"title":"Аккумуляторы для Bosch GKS 18V-57","body":"Совместимы GBA 18V / ProCORE18V (BAT609/618/619).","topic":"DIY","sub":"Аккумуляторы","type":["reference"]}
}
```

Rules for the card:
- `dataModel` holds your draft; the user edits the fields in place. `type` is a **list** (single
  selection → one element).
- Keep `body` as markdown. If topic/sub aren't clear from context, leave them empty — the user
  picks. Don't invent a topic the user didn't imply.
- After calling `render_ui`, **stop and wait**. Do not save yet, do not narrate the JSON.

**Step 2 — on confirm, save.** The click arrives as a new user message:
`Действие в интерфейсе: save_note {"title":"…","body":"…","topic":"…","sub":"…","type":["reference"]}`.
Take those (edited) values — `type` is a list, use its first element — and POST them:

```sh
curl -sf -X POST "$BRIGADE_API_URL/brigade.v1.AgentBridgeService/CreateMemoryNote" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"sessionId\": \"$BRIGADE_SESSION_ID\", \"topic\": \"DIY\", \"sub\": \"Аккумуляторы\", \"title\": \"Аккумуляторы для Bosch GKS 18V-57\", \"body\": \"Совместимы GBA 18V / ProCORE18V (BAT609/618/619).\", \"type\": \"reference\", \"tags\": [\"bosch\"]}"
```

The response is `{"id": "...", "commitSha": "..."}` — `commitSha` proves it's durably pushed.
Tell the user it's saved and into which topic/subtopic.

## Distill a session into layered memory

When asked to "save this session to memory" (or the session got long and valuable), skip the
card — save directly with the `curl` above, both layers (pick a fitting `topic`):

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
