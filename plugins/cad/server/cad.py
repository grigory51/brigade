from __future__ import annotations

import base64
import json
import os
import re
import sys
import threading
from pathlib import Path
from typing import Any
from mcp.server import MCPServer
from mcp.server.apps import Apps

APP_URI = "ui://cad/app.html"
workspace = Path(os.environ.get("BRIGADE_WORKSPACE", os.getcwd())).resolve()
session_id = os.environ.get("BRIGADE_SESSION_ID", "")
lock = threading.Lock()
apps = Apps()


def file_url(path: Path) -> str:
    relative = path.relative_to(workspace).as_posix()
    return f"/api/sessions/{session_id}/files/{relative}"


def current_model() -> dict[str, Any]:
    state_path = workspace / ".brigade-cad.json"
    if not state_path.exists():
        return {"status": "empty"}
    return json.loads(state_path.read_text(encoding="utf-8"))


@apps.tool(
    name="cad.open",
    title="Open CAD workspace",
    description="Open the persistent CAD scene for this Brigade session.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def open_cad() -> dict[str, Any]:
    return current_model()


@apps.tool(
    name="cad.preview",
    title="Read CAD preview",
    description="Read the current GLB preview for the CAD app.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def read_preview() -> dict[str, Any]:
    with lock:
        state = current_model()
        name = state.get("name")
        if state.get("status") != "ready" or not isinstance(name, str):
            return {"status": "empty"}
        data = base64.b64encode((workspace / f"{name}.glb").read_bytes()).decode("ascii")
        return {"status": "ready", "mimeType": "model/gltf-binary", "data": data}


def bundled(path: str) -> Path:
    if root := getattr(sys, "_MEIPASS", None):
        return Path(root) / path
    return Path(__file__).resolve().parent.parent / "ui/dist/index.html"


apps.add_html_resource(APP_URI, bundled("ui/mcp-app.html").read_text(encoding="utf-8"), title="Brigade CAD")
mcp = MCPServer("Brigade CAD", extensions=[apps])


@mcp.tool(
    name="cad.build",
    title="Build parametric CAD",
    description=(
        "Execute build123d source that assigns the final Shape to `result`. "
        "Writes the source, canonical STEP, and browser GLB preview into the session workspace."
    ),
)
def build_cad(source: str, name: str = "model", linear_tolerance: float = 0.1, angular_tolerance: float = 0.2) -> dict[str, Any]:
    import trimesh
    from build123d import Shape, export_step

    safe_name = re.sub(r"[^A-Za-z0-9._-]+", "-", name).strip(".-") or "model"
    source_path = workspace / f"{safe_name}.py"
    step_path = workspace / f"{safe_name}.step"
    glb_path = workspace / f"{safe_name}.glb"
    temporary_source = workspace / f".{safe_name}.py.tmp"
    temporary_step = workspace / f".{safe_name}.step.tmp"
    temporary_glb = workspace / f".{safe_name}.glb.tmp"
    with lock:
        try:
            temporary_source.write_text(source, encoding="utf-8")
            namespace: dict[str, Any] = {}
            exec(compile("from build123d import *\n" + source, str(source_path), "exec"), namespace)
            result = namespace.get("result")
            if not isinstance(result, Shape):
                raise ValueError("build123d source must assign a Shape to `result`")
            export_step(result, temporary_step)
            vertices, triangles = result.tessellate(linear_tolerance, angular_tolerance)
            mesh = trimesh.Trimesh(
                vertices=[tuple(vertex) for vertex in vertices],
                faces=[tuple(triangle) for triangle in triangles],
                process=False,
            )
            mesh.export(temporary_glb, file_type="glb")
            temporary_source.replace(source_path)
            temporary_step.replace(step_path)
            temporary_glb.replace(glb_path)
        finally:
            temporary_source.unlink(missing_ok=True)
            temporary_step.unlink(missing_ok=True)
            temporary_glb.unlink(missing_ok=True)
        state = {
            "status": "ready",
            "name": safe_name,
            "revision": glb_path.stat().st_mtime_ns,
            "sourceUrl": file_url(source_path),
            "stepUrl": file_url(step_path),
            "previewUrl": file_url(glb_path) + "?inline=1",
        }
        temporary = workspace / ".brigade-cad.json.tmp"
        temporary.write_text(json.dumps(state), encoding="utf-8")
        temporary.replace(workspace / ".brigade-cad.json")
        return state


if __name__ == "__main__":
    mcp.run()
