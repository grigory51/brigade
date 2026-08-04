---
name: brigade-files
description: Publish or preview files created in the session workspace. Use when the user asks to download a file or to show, open, view, or preview a 3D model, CAD model, SCAD, STEP, STL, GLB, or GLTF.
---

# Brigade file downloads

Call the built-in `brigade` MCP tool `publish_file` for every file the user should download.
The tool renders a download card in chat. Make it the final action of the turn: do not repeat its
result or write text afterward. Never link an absolute local path or a `file://` URL.

For a 3D model, publish a `.glb` file to show an interactive viewer. For CAD, keep the
manufacturing source as STEP and pass a GLB rendering through the optional `preview` argument:
`publish_file({path: "model.step", preview: "model.glb"})`.

When the user asks to show an existing model, inspect the workspace for the model directly;
do not search for another skill. A `.glb`/`.gltf` can be published as-is. For `.scad`, `.step`,
`.stp`, or `.stl`, create a GLB preview with available project tools, then publish the source
with that preview. If conversion is impossible in the current environment, state which tool
is missing instead of continuing to search for skills.
