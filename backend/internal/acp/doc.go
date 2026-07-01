// Package acp реализует ACP-клиент brigade.
//
// Клиент спавнит subprocess adapter'а (claude-agent-acp — agent-side ACP поверх Claude
// Agent SDK), устанавливает соединение через acp-go-sdk (ClientSideConnection), проходит
// Initialize/NewSession и обслуживает Prompt. Реализует client-side интерфейс ACP
// (SessionUpdate, RequestPermission, Read/WriteTextFile, терминальные no-op'ы).
//
// Поток событий агента транслируется из ACP в AG-UI (см. пакет agui) и доставляется
// клиенту через EventSink. Permission-flow блокируется до ответа клиента через
// PermissionResolver. Реестр кастомных сниппетов (frontend-tools), присланный фронтом,
// пробрасывается агенту при Prompt в зарезервированном поле _meta.
//
// Связывание клиента с конкретным транспортом (AG-UI поверх SSE) выполняет пакет
// transport/agui. Связывание с реестром живых сессий (store, resume) — пакет session.
package acp
