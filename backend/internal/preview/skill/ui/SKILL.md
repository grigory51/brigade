---
name: brigade-ui
description: Render custom interactive interfaces directly in Brigade ACP chat. Use when the user asks for a UI, card, form, choice, mockup, dashboard, prototype, or asks whether A2UI, A2U, generative UI, or custom interfaces are available.
---

# Brigade generative UI

This Codex session runs inside Brigade ACP. Brigade can render custom interfaces directly
in the chat through the built-in `brigade` MCP server.

- For an arbitrary interface, find and call the `render_ui` tool. MCP tools may be deferred,
  so use tool search when it is not already visible.
- For a simple choice, use `show_choice`.
- Do not claim that A2UI/A2U or custom UI is unavailable before searching for `render_ui`.
- Use these chat-native tools instead of building and publishing a standalone web preview,
  unless the user explicitly asks for a website or a separately shareable preview.
- After rendering an interactive UI, wait for the user's action instead of duplicating it
  as prose.
