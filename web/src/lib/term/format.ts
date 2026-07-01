import {
  SessionKind,
  SessionMode,
  SessionStatus,
} from "@/api/gen/brigade/v1/session_pb";

// Человекочитаемые ярлыки перечислений сессии. Держим в одном месте, чтобы не
// дублировать switch по компонентам.

export function kindLabel(kind: SessionKind): string {
  switch (kind) {
    case SessionKind.CLI:
      return "CLI";
    case SessionKind.ACP:
      return "ACP";
    default:
      return "—";
  }
}

export function modeLabel(mode: SessionMode): string {
  switch (mode) {
    case SessionMode.LOCAL:
      return "local";
    case SessionMode.DOCKER:
      return "docker";
    default:
      return "—";
  }
}

export function statusLabel(status: SessionStatus): string {
  switch (status) {
    case SessionStatus.RUNNING:
      return "running";
    case SessionStatus.STOPPED:
      return "stopped";
    case SessionStatus.FAILED:
      return "failed";
    default:
      return "—";
  }
}

// Цветовые токены бейджа статуса (Tailwind-классы). UNSPECIFIED трактуем как
// stopped. Применяется к компоненту Badge через className.
export function statusBadgeClass(status: SessionStatus): string {
  switch (status) {
    case SessionStatus.RUNNING:
      return "border-transparent bg-success/15 text-success";
    case SessionStatus.FAILED:
      return "border-transparent bg-destructive/15 text-destructive";
    default:
      return "border-transparent bg-muted text-muted-foreground";
  }
}

// Маршрут экрана сессии. Режим (CLI или ACP) определяется не URL, а данными сессии
// (см. SessionGuard), поэтому путь единый для обоих типов.
export function sessionRoute(id: string): string {
  return `/s/${id}`;
}

// Unix-секунды (bigint из proto int64) → локальная дата-время.
export function formatCreatedAt(unixSec: bigint): string {
  if (!unixSec) return "";
  const d = new Date(Number(unixSec) * 1000);
  return d.toLocaleString();
}
