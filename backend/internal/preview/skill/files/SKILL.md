---
name: brigade-files
description: Publish files created in the session workspace as downloadable Brigade links. Use when the user asks to download, receive, or get a generated file or archive.
---

# Brigade file downloads

Call the built-in `brigade` MCP tool `publish_file` for every file the user should download.
The tool renders a download card in chat. Make it the final action of the turn: do not repeat its
result or write text afterward. Never link an absolute local path or a `file://` URL.

For a 3D model, publish a `.glb` file to show an interactive viewer. For CAD, keep the
manufacturing source as STEP and pass a GLB rendering through the optional `preview` argument:
`publish_file({path: "model.step", preview: "model.glb"})`.
