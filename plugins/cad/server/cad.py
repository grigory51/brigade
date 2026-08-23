from __future__ import annotations

import base64
import fcntl
import json
import os
import re
import shutil
import sys
import threading
from contextlib import contextmanager
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Iterator

from mcp.server import MCPServer
from mcp.server.apps import Apps

APP_URI = "ui://cad/app.html"
STATE_VERSION = 2
MAX_REVISIONS = 20
workspace = Path(os.environ.get("BRIGADE_WORKSPACE", os.getcwd())).resolve()
session_id = os.environ.get("BRIGADE_SESSION_ID", "")
state_path = workspace / ".brigade-cad.json"
cad_dir = workspace / ".brigade-cad"
revisions_dir = cad_dir / "revisions"
thread_lock = threading.Lock()
apps = Apps()


def file_url(path: Path) -> str:
    relative = path.relative_to(workspace).as_posix()
    return f"/api/sessions/{session_id}/files/{relative}"


def empty_state() -> dict[str, Any]:
    return {
        "schemaVersion": STATE_VERSION,
        "status": "empty",
        "pipeline": {"stage": "empty", "status": "idle"},
        "parameters": [],
        "validation": {"status": "pending", "checks": []},
        "revisions": [],
    }


def current_model() -> dict[str, Any]:
    if not state_path.exists():
        return empty_state()
    try:
        state = json.loads(state_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        return {**empty_state(), "status": "error", "error": f"Cannot read CAD state: {error}"}
    state.setdefault("schemaVersion", 1)
    state.setdefault("parameters", [])
    state.setdefault("validation", {"status": "pending", "checks": []})
    state.setdefault("revisions", [])
    state.setdefault("pipeline", {"stage": state.get("status", "empty"), "status": "idle"})
    return state


def write_state(state: dict[str, Any]) -> None:
    temporary = workspace / ".brigade-cad.json.tmp"
    temporary.write_text(json.dumps(state, ensure_ascii=False), encoding="utf-8")
    temporary.replace(state_path)


@contextmanager
def workspace_lock() -> Iterator[None]:
    cad_dir.mkdir(parents=True, exist_ok=True)
    with thread_lock, (cad_dir / "build.lock").open("a+") as lock_file:
        fcntl.flock(lock_file, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lock_file, fcntl.LOCK_UN)


def normalize_parameters(
    definitions: list[dict[str, Any]] | None,
    values: dict[str, Any] | None = None,
) -> list[dict[str, Any]]:
    normalized: list[dict[str, Any]] = []
    seen: set[str] = set()
    for raw in definitions or []:
        parameter_id = raw.get("id")
        if not isinstance(parameter_id, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", parameter_id):
            raise ValueError("CAD parameter id must be a Python identifier")
        if parameter_id in seen:
            raise ValueError(f"duplicate CAD parameter {parameter_id!r}")
        seen.add(parameter_id)
        kind = raw.get("type", "number")
        value = (values or {}).get(parameter_id, raw.get("value"))
        if kind == "number":
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise ValueError(f"CAD parameter {parameter_id!r} must be a number")
            minimum = raw.get("min")
            maximum = raw.get("max")
            if isinstance(minimum, (int, float)) and value < minimum:
                raise ValueError(f"CAD parameter {parameter_id!r} is below {minimum}")
            if isinstance(maximum, (int, float)) and value > maximum:
                raise ValueError(f"CAD parameter {parameter_id!r} exceeds {maximum}")
        elif kind == "boolean":
            if not isinstance(value, bool):
                raise ValueError(f"CAD parameter {parameter_id!r} must be boolean")
        else:
            raise ValueError(f"unsupported CAD parameter type {kind!r}")
        normalized.append({
            "id": parameter_id,
            "label": raw.get("label") or parameter_id.replace("_", " ").title(),
            "type": kind,
            "value": value,
            **({"min": raw["min"]} if "min" in raw else {}),
            **({"max": raw["max"]} if "max" in raw else {}),
            **({"step": raw["step"]} if "step" in raw else {}),
            **({"unit": raw["unit"]} if raw.get("unit") else {}),
            **({"description": raw["description"]} if raw.get("description") else {}),
        })
    unknown = set(values or {}) - seen
    if unknown:
        raise ValueError(f"unknown CAD parameters: {', '.join(sorted(unknown))}")
    return normalized


def validate_shape(result: Any, mesh: Any) -> dict[str, Any]:
    bounds = result.bounding_box().size
    solids = list(result.solids())
    volumes = [float(solid.volume) for solid in solids]
    volume = sum(volumes)
    checks = [
        {
            "id": "topology",
            "label": "Valid topology",
            "status": "pass" if result.is_valid else "fail",
            "detail": f"{len(solids)} solid{'s' if len(solids) != 1 else ''}",
        },
        {
            "id": "volume",
            "label": "Positive volume",
            "status": "pass" if volumes and all(item > 0 for item in volumes) else "fail",
            "detail": f"{volume:.2f} mm³",
        },
        {
            "id": "mesh",
            "label": "Closed preview mesh",
            "status": "pass" if mesh.is_volume else "fail",
            "detail": f"{len(mesh.faces)} triangles",
        },
    ]
    return {
        "status": "pass" if all(check["status"] == "pass" for check in checks) else "fail",
        "checks": checks,
        "bounds": {"x": float(bounds.X), "y": float(bounds.Y), "z": float(bounds.Z), "unit": "mm"},
        "solidCount": len(solids),
        "volume": volume,
    }


def public_state(state: dict[str, Any], *, source: bool = False) -> dict[str, Any]:
    result = dict(state)
    if not source:
        result.pop("source", None)
        result.pop("revisions", None)
    return result


@apps.tool(
    name="cad.open",
    title="Open CAD workspace",
    description="Open the persistent CAD scene, parameters, checks, and revisions for this session.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def open_cad() -> dict[str, Any]:
    return public_state(current_model(), source=True)


@apps.tool(
    name="cad.preview",
    title="Read CAD preview",
    description="Read the current GLB preview for the CAD app.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def read_preview() -> dict[str, Any]:
    with workspace_lock():
        state = current_model()
        name = state.get("name")
        if not isinstance(name, str) or not (workspace / f"{name}.glb").is_file():
            return {"status": state.get("status", "empty")}
        data = base64.b64encode((workspace / f"{name}.glb").read_bytes()).decode("ascii")
        return {"status": "ready", "mimeType": "model/gltf-binary", "data": data}


@apps.tool(
    name="cad.update_parameters",
    title="Update CAD parameters",
    description="Rebuild the current model with edited parameter values.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def update_parameters(values: dict[str, Any]) -> dict[str, Any]:
    state = current_model()
    if state.get("status") not in {"ready", "error"} or not isinstance(state.get("source"), str):
        raise ValueError("CAD workspace has no editable model")
    build_cad(
        state["source"],
        str(state.get("name") or "model"),
        state.get("parameters", []),
        values,
    )
    return open_cad()


@apps.tool(
    name="cad.rebuild",
    title="Rebuild CAD source",
    description="Compile edited build123d source and keep it as a new revision.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def rebuild_source(source: str) -> dict[str, Any]:
    state = current_model()
    build_cad(
        source,
        str(state.get("name") or "model"),
        state.get("parameters", []),
    )
    return open_cad()


@apps.tool(
    name="cad.restore",
    title="Restore CAD revision",
    description="Restore a previous CAD revision as a new current revision.",
    resource_uri=APP_URI,
    visibility=["app"],
)
def restore_revision(revision: int) -> dict[str, Any]:
    metadata_path = revisions_dir / str(revision) / "revision.json"
    if not metadata_path.is_file():
        raise ValueError(f"CAD revision {revision} does not exist")
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    source = (metadata_path.parent / "model.py").read_text(encoding="utf-8")
    build_cad(
        source,
        str(metadata.get("name") or "model"),
        metadata.get("parameters", []),
    )
    return open_cad()


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
        "Execute build123d source that assigns the final Shape to `result`. The source may read editable "
        "values from the injected `params` dictionary. Writes canonical STEP and browser GLB artifacts, "
        "runs deterministic geometry checks, and records a revision."
    ),
)
def build_cad(
    source: str,
    name: str = "model",
    parameters: list[dict[str, Any]] | None = None,
    values: dict[str, Any] | None = None,
    linear_tolerance: float = 0.1,
    angular_tolerance: float = 0.2,
) -> dict[str, Any]:
    import trimesh
    from build123d import Shape, export_step

    safe_name = re.sub(r"[^A-Za-z0-9._-]+", "-", name).strip(".-") or "model"
    source_path = workspace / f"{safe_name}.py"
    step_path = workspace / f"{safe_name}.step"
    glb_path = workspace / f"{safe_name}.glb"
    temporary_source = workspace / f".{safe_name}.py.tmp"
    temporary_step = workspace / f".{safe_name}.step.tmp"
    temporary_glb = workspace / f".{safe_name}.glb.tmp"
    with workspace_lock():
        previous = current_model()
        normalized = normalize_parameters(parameters, values)
        write_state({
            **previous,
            "schemaVersion": STATE_VERSION,
            "status": "building",
            "pipeline": {"stage": "build", "status": "running", "startedAt": datetime.now(UTC).isoformat()},
            "error": "",
        })
        try:
            temporary_source.write_text(source, encoding="utf-8")
            namespace: dict[str, Any] = {
                "params": {parameter["id"]: parameter["value"] for parameter in normalized},
            }
            exec(compile("from build123d import *\n" + source, str(source_path), "exec"), namespace)
            result = namespace.get("result")
            if not isinstance(result, Shape):
                raise ValueError("build123d source must assign a Shape to `result`")
            export_step(result, temporary_step)
            vertices, triangles = result.tessellate(linear_tolerance, angular_tolerance)
            mesh = trimesh.Trimesh(
                vertices=[tuple(vertex) for vertex in vertices],
                faces=[tuple(triangle) for triangle in triangles],
                process=True,
            )
            mesh.export(temporary_glb, file_type="glb")
            validation = validate_shape(result, mesh)
            temporary_source.replace(source_path)
            temporary_step.replace(step_path)
            temporary_glb.replace(glb_path)

            revision = int(previous.get("revision") or 0) + 1
            created_at = datetime.now(UTC).isoformat()
            revision_dir = revisions_dir / str(revision)
            revision_dir.mkdir(parents=True, exist_ok=False)
            shutil.copy2(source_path, revision_dir / "model.py")
            shutil.copy2(step_path, revision_dir / "model.step")
            shutil.copy2(glb_path, revision_dir / "model.glb")
            metadata = {
                "revision": revision,
                "name": safe_name,
                "createdAt": created_at,
                "parameters": normalized,
                "validation": validation,
            }
            (revision_dir / "revision.json").write_text(json.dumps(metadata, ensure_ascii=False), encoding="utf-8")
            revisions = [
                {
                    "revision": revision,
                    "createdAt": created_at,
                    "validationStatus": validation["status"],
                },
                *[item for item in previous.get("revisions", []) if item.get("revision") != revision],
            ][:MAX_REVISIONS]
            retained = {str(item["revision"]) for item in revisions}
            if revisions_dir.exists():
                for child in revisions_dir.iterdir():
                    if child.is_dir() and child.name not in retained:
                        shutil.rmtree(child)
            state = {
                "schemaVersion": STATE_VERSION,
                "status": "ready",
                "name": safe_name,
                "revision": revision,
                "createdAt": created_at,
                "source": source,
                "parameters": normalized,
                "validation": validation,
                "revisions": revisions,
                "pipeline": {"stage": "validated", "status": "complete", "completedAt": created_at},
                "sourceUrl": file_url(source_path),
                "stepUrl": file_url(step_path),
                "previewUrl": file_url(glb_path) + "?inline=1",
            }
            write_state(state)
            return public_state(state)
        except Exception as error:
            write_state({
                **previous,
                "schemaVersion": STATE_VERSION,
                "status": "error",
                "pipeline": {"stage": "build", "status": "failed", "completedAt": datetime.now(UTC).isoformat()},
                "error": str(error),
            })
            raise
        finally:
            temporary_source.unlink(missing_ok=True)
            temporary_step.unlink(missing_ok=True)
            temporary_glb.unlink(missing_ok=True)


if __name__ == "__main__":
    mcp.run()
