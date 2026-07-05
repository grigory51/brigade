import { createContext } from "react";
import type { A2uiState } from "../useAcpRuntime";

// A2uiContext доносит процессор A2UI-поверхностей сессии до рендереров tool-call'ов
// внутри Thread (ToolFallback — бэкенд-синтезированные карточки diff; RenderUiCard —
// generative UI от агента). Карточка с готовой поверхностью (surfaceId = toolCallId)
// рендерится A2UI-рендерером; version в значении контекста заставляет потребителей
// ре-рендериться при появлении/изменении поверхностей (onSurfaceCreated/Deleted).
export const A2uiContext = createContext<A2uiState | null>(null);
