# Repository Guidelines

## Project Overview

Brigade runs coding agents in CLI terminal or ACP chat sessions, either as local
processes or per-session Docker containers. The Go backend serves ConnectRPC,
streaming transports, and an embedded React application as one binary. A Kotlin
Multiplatform client lives alongside the main application.

## Project Structure

- `backend/cmd/brigade/` assembles the service; domain packages and colocated tests
  are under `backend/internal/`.
- `web/src/` contains the React/TypeScript client. Put reusable UI in `components/`,
  product behavior in `features/`, and transport code in `api/`.
- `proto/brigade/v1/` is the only source of truth for API contracts.
  `backend/gen/go/` and `web/src/api/gen/` are generated; never edit them manually.
- `mobile/` contains the KMP shared client and application shells.
- `packaging/`, `site/`, and `docs/` contain deployment and presentation
  assets.

## Build and Development

Run commands from the repository root:

- `make build` builds the web app, embeds it, and produces `backend/bin/brigade`.
- `make run` rebuilds and starts the service with `backend/config.yaml`.
- `make proto` regenerates Go and TypeScript code from protobuf definitions.
- `make test` runs `go test ./...`; `make vet` runs Go static analysis.
- `make build-web`, `make build-mobile`, and `make app` build individual targets.

For frontend iteration, run `make -C web install`, then `make -C web dev`; validate
with `make -C web lint`. Run a focused Go test with, for example,
`cd backend && go test ./internal/auth -run TestJWT`.

If Go reports a tool-version mismatch, remove a manually exported `GOROOT`; the Go
toolchain determines it automatically.

## Architecture and API Boundaries

Use ConnectRPC for new request/response endpoints: change the relevant `.proto`,
run `make proto`, then implement the generated interface. Raw transports are reserved
for protocols Connect cannot represent: AG-UI SSE at `/api/ag-ui/run` and terminal
WebSockets under `/ws/`.

AG-UI is the ACP chat event protocol. A2UI is a UI-card feature carried inside AG-UI
custom events; do not treat them as interchangeable layers.

`backend/internal/session.Registry` owns live sessions and persistence. Preserve
restore behavior: a local session resumes its agent process, while a Docker session
reattaches by `brigade.session.id`; one failed restore must not prevent server startup.
Database migrations belong in `backend/internal/store/migrations/` and run at startup.

## Coding and Testing Conventions

Format Go with `gofmt`; use lowercase package names, exported `PascalCase` identifiers,
`*_test.go` files, and `TestXxx` functions. TypeScript uses two-space indentation,
`PascalCase` components, and `camelCase` functions and hooks. Follow nearby code rather
than introducing a new pattern or dependency.

Add focused unit tests for behavior changes and regression tests for fixes. Before
submitting, run `make test`, `make vet`, and, for web changes, `make -C web lint`.
There is no configured coverage threshold.

When diagnosis lacks enough evidence, propose a minimal temporary debug command,
dump, or metric that the user can run and share. Prefer adding a focused diagnostic
path over repeating speculative fixes or a long sequence of manual shell commands.

## Configuration and Security

Copy `backend/config.example.yaml` to `backend/config.yaml` for local use. Environment
overrides use the `BRIGADE_` prefix and `__` for nesting, for example
`BRIGADE_JWT__SECRET`. Never commit populated configuration, databases, tokens, SSH
keys, or decrypted MCP secrets.

Claude tokens and MCP configuration are per-user settings, not global configuration.
Keep secrets server-side and preserve the existing vault/reference flow. Carefully
review Docker socket, runtime-volume, and workspace-mount changes; agent runtime
volumes are intentionally read-only.

## Commits, Pull Requests, and Releases

Use scoped Conventional Commits, as in `feat(agent): ...`, `fix(web): ...`, or
`refactor(daemon): ...`. Keep commits focused and subjects concise.

Project skills live in `.agents/skills/`. `.claude/skills` points to the same
directory so Codex and Claude Code use one shared definition. Add or update skills
only in `.agents/skills/`; do not replace the compatibility symlink with copies.

Pull requests should describe the problem and solution, identify affected modules,
list verification commands, and link relevant issues. Include screenshots for visible
UI changes and call out migrations, protobuf changes, configuration, or deployment
impact.

Git tags are the version source of truth. Do not edit package versions or create
release tags manually. From a clean tree, use `make release`, or set
`BUMP=minor`/`BUMP=major`; the target creates and pushes an annotated semver tag.
