import { SessionKind } from "@/api/gen/brigade/v1/session_pb";

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

// Маршрут экрана сессии. Режим (CLI или ACP) определяется не URL, а данными сессии
// (см. SessionGuard), поэтому путь единый для обоих типов.
export function sessionRoute(id: string): string {
  return `/s/${id}`;
}
