import { useCallback, useState } from "react";
import { Loader2, RotateCw, WifiOff } from "lucide-react";
import { sessionClient } from "@/api/client";
import { SessionStatus } from "@/api/gen/brigade/v1/session_pb";
import {
  TerminalView,
  type TermConnState,
} from "@/features/terminal/TerminalView";
import { useSessionHeader } from "@/features/sessions/SessionHeaderSlot";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

// "ended" — сессия завершена на сервере (агент вышел, например по /quit):
// переподключение невозможно, поэтому показываем терминальное состояние без кнопки
// reconnect. Остальные состояния относятся к самому WebSocket-соединению и приходят
// из TerminalView.
type ConnState = TermConnState | "ended";

// CliSession монтируется из SessionGuard только при найденной сессии — иначе
// WS-эффекты терминала открыли бы соединение в никуда ещё до показа 404.
export function CliSession({ sessionId }: { sessionId: string | undefined }) {
  const [conn, setConn] = useState<ConnState>("connecting");
  // Счётчик попыток переподключения для ручного reconnect-триггера.
  const [attempt, setAttempt] = useState(0);

  const onConnChange = useCallback(
    (c: TermConnState) => {
      setConn(c);
      if (c !== "closed" || !sessionId) return;
      // Соединение закрылось. Уточняем у сервера статус сессии: если агент
      // завершился (stopped/failed), переподключаться некуда — показываем
      // терминальное состояние "ended". Иначе это обрыв канала — оставляем
      // "closed" с возможностью ручного reconnect.
      void sessionClient
        .get({ sessionId })
        .then((res) => {
          const status = res.session?.status;
          if (
            status === SessionStatus.STOPPED ||
            status === SessionStatus.FAILED
          ) {
            setConn("ended");
          }
        })
        .catch(() => {
          // Статус не удалось получить — оставляем "closed".
        });
    },
    [sessionId],
  );

  // Title/right публикуются в под-хедер оболочки (см. SessionHeaderSlot): сам экран
  // возвращает только содержимое — терминал, без внешней шапки.
  useSessionHeader({
    title: <span className="font-mono text-xs">{sessionId}</span>,
    right: (
      <ConnBadge conn={conn} onReconnect={() => setAttempt((a) => a + 1)} />
    ),
  });

  if (!sessionId) return null;
  return (
    <TerminalView
      kind="terminal"
      sessionId={sessionId}
      attempt={attempt}
      onConnChange={onConnChange}
    />
  );
}

function ConnBadge({
  conn,
  onReconnect,
}: {
  conn: ConnState;
  onReconnect: () => void;
}) {
  if (conn === "open") {
    return (
      <Badge
        variant="outline"
        className="gap-1.5 border-transparent bg-success/15 text-success"
      >
        <span className="size-1.5 rounded-full bg-current" />
        подключено
      </Badge>
    );
  }
  if (conn === "connecting") {
    return (
      <Badge variant="outline" className="gap-1.5 text-muted-foreground">
        <Loader2 className="size-3 animate-spin" />
        подключение
      </Badge>
    );
  }
  if (conn === "ended") {
    // Сессия завершена на сервере — переподключаться некуда, кнопку не показываем.
    return (
      <Badge variant="outline" className="gap-1.5 text-muted-foreground">
        сессия завершена
      </Badge>
    );
  }
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onReconnect}
      className="text-destructive"
    >
      {conn === "error" ? (
        <WifiOff className="size-4" />
      ) : (
        <RotateCw className="size-4" />
      )}
      {conn === "error" ? "ошибка — переподключить" : "переподключить"}
    </Button>
  );
}
