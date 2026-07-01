import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConnectError, Code } from "@connectrpc/connect";
import { sessionClient } from "@/api/client";
import { SessionKind } from "@/api/gen/brigade/v1/session_pb";
import { RouteSpinner } from "@/lib/RouteSpinner";
import { Button } from "@/components/ui/button";
import { CliSession } from "@/features/cli/CliPage";
import { AcpSession } from "@/features/acp/AcpPage";

type Existence = "checking" | "found" | "notfound" | "error";

type SessionLookup = { state: Existence; kind?: SessionKind };

// useSessionLookup проверяет наличие сессии по идентификатору через unary-вызов
// перед открытием WebSocket. Это позволяет показать 404 на заход по несуществующему
// id, а не пустой терминал/чат, который молча подключился бы в никуда. Возвращает
// также kind найденной сессии, чтобы выбрать экран (CLI или ACP) без второго запроса.
function useSessionLookup(sessionId: string | undefined): SessionLookup {
  const [lookup, setLookup] = useState<SessionLookup>({ state: "checking" });

  useEffect(() => {
    if (!sessionId) {
      setLookup({ state: "notfound" });
      return;
    }
    let cancelled = false;
    setLookup({ state: "checking" });
    sessionClient
      .get({ sessionId })
      .then((resp) => {
        if (!cancelled) {
          setLookup({ state: "found", kind: resp.session?.kind });
        }
      })
      .catch((err) => {
        if (cancelled) return;
        // NotFound и PermissionDenied (чужая сессия трактуется бэкендом как
        // отсутствующая) одинаково означают «такой сессии у пользователя нет».
        const code = err instanceof ConnectError ? err.code : undefined;
        setLookup({
          state:
            code === Code.NotFound || code === Code.PermissionDenied
              ? "notfound"
              : "error",
        });
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId]);

  return lookup;
}

// SessionGuard проверяет существование сессии и по её kind монтирует нужный экран
// (CLI или ACP); иначе показывает экран загрузки, 404 или ошибку. Единая точка входа
// для маршрута /s/:sessionId — режим сессии берётся из данных, а не из URL.
export function SessionGuard({ sessionId }: { sessionId: string | undefined }) {
  const { state, kind } = useSessionLookup(sessionId);

  if (state === "checking") {
    return <RouteSpinner />;
  }
  if (state === "notfound") {
    return <SessionNotFound />;
  }
  if (state === "error") {
    return <SessionLoadError />;
  }
  // sessionId здесь гарантированно определён: пустой id даёт notfound выше.
  return kind === SessionKind.ACP ? (
    <AcpSession sessionId={sessionId!} />
  ) : (
    <CliSession sessionId={sessionId} />
  );
}

// Состояния 404/ошибки рендерятся внутри области контента оболочки (SidebarInset),
// поэтому занимают только её высоту, без собственной шапки.
function SessionNotFound() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
      <div className="text-5xl font-semibold text-muted-foreground">404</div>
      <p className="max-w-sm text-sm text-muted-foreground">
        Сессия не найдена. Возможно, она была удалена или ссылка устарела.
      </p>
      <Button asChild variant="outline">
        <Link to="/sessions">К списку сессий</Link>
      </Button>
    </div>
  );
}

function SessionLoadError() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
      <p className="max-w-sm text-sm text-muted-foreground">
        Не удалось проверить сессию. Проверьте соединение и обновите страницу.
      </p>
      <Button asChild variant="outline">
        <Link to="/sessions">К списку сессий</Link>
      </Button>
    </div>
  );
}
