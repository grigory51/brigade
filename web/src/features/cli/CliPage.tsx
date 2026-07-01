import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2, RotateCw, WifiOff } from "lucide-react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { streamUrl } from "@/api/ws";
import { sessionClient } from "@/api/client";
import { SessionStatus } from "@/api/gen/brigade/v1/session_pb";
import { useSessionHeader } from "@/features/sessions/SessionHeaderSlot";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

// "ended" — сессия завершена на сервере (агент вышел, например по /quit):
// переподключение невозможно, поэтому показываем терминальное состояние без кнопки
// reconnect. Остальные состояния относятся к самому WebSocket-соединению.
type ConnState = "connecting" | "open" | "closed" | "error" | "ended";

// Палитра терминала согласована с тёмной темой brigade (см. globals.css).
const TERMINAL_THEME = {
  background: "#15191f",
  foreground: "#e6edf3",
  cursor: "#4493f8",
  selectionBackground: "rgba(68,147,248,0.3)",
  black: "#15191f",
  red: "#f85149",
  green: "#3fb950",
  yellow: "#d29922",
  blue: "#4493f8",
  magenta: "#bc8cff",
  cyan: "#39c5cf",
  white: "#b1bac4",
  brightBlack: "#6e7681",
  brightRed: "#ff7b72",
  brightGreen: "#56d364",
  brightYellow: "#e3b341",
  brightBlue: "#79c0ff",
  brightMagenta: "#d2a8ff",
  brightCyan: "#56d4dd",
  brightWhite: "#f0f6fc",
} as const;

// CliSession монтируется из SessionGuard только при найденной сессии — иначе
// WS-эффекты терминала открыли бы соединение в никуда ещё до показа 404.
export function CliSession({ sessionId }: { sessionId: string | undefined }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [conn, setConn] = useState<ConnState>("connecting");
  // Счётчик попыток переподключения для ручного reconnect-триггера.
  const [attempt, setAttempt] = useState(0);

  // Отправляет на сервер текущий размер терминала, чтобы pty совпадал с вьюпортом.
  const sendResize = useCallback(() => {
    const term = termRef.current;
    const ws = wsRef.current;
    if (!term || !ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(
      JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }),
    );
  }, []);

  // Инициализация xterm один раз: терминал переживает реконнекты, поэтому история
  // вывода не теряется при переподключении.
  useEffect(() => {
    if (!hostRef.current) return;
    const term = new Terminal({
      fontFamily:
        '"SFMono-Regular", "JetBrains Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      theme: TERMINAL_THEME,
      cursorBlink: true,
      scrollback: 5000,
      convertEol: false,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    // Подгонка размера терминала под контейнер. Прямой вызов fit() из колбэка
    // ResizeObserver порождает бесконечную петлю: fit меняет геометрию xterm,
    // из-за чего у контейнера то появляется, то исчезает вертикальный скроллбар,
    // его ширина меняется на ширину скроллбара, observer срабатывает снова — и так
    // без остановки («дрожание»). Поэтому: (1) измеряем в requestAnimationFrame, а
    // не синхронно в колбэке; (2) применяем новый размер только если число колонок
    // или строк реально изменилось — это разрывает петлю на стабильном размере.
    let rafId = 0;
    const applyFit = () => {
      rafId = 0;
      const t = termRef.current;
      if (!t) return;
      const prevCols = t.cols;
      const prevRows = t.rows;
      try {
        fit.fit();
      } catch {
        // Контейнер мог быть временно скрыт; пропускаем измерение.
        return;
      }
      if (t.cols !== prevCols || t.rows !== prevRows) {
        sendResize();
      }
    };
    const ro = new ResizeObserver(() => {
      if (rafId === 0) rafId = requestAnimationFrame(applyFit);
    });
    ro.observe(hostRef.current);

    return () => {
      if (rafId !== 0) cancelAnimationFrame(rafId);
      ro.disconnect();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [sendResize]);

  // Подключение к WS терминала: тикет → URL → бинарь в терминал, ввод JSON-ом.
  useEffect(() => {
    if (!sessionId) return;
    const term = termRef.current;
    if (!term) return;

    let closed = false;
    let socket: WebSocket | null = null;
    setConn("connecting");

    void (async () => {
      try {
        const url = await streamUrl("terminal", sessionId);
        if (closed) return;
        socket = new WebSocket(url);
        socket.binaryType = "arraybuffer";
        wsRef.current = socket;

        socket.onopen = () => {
          setConn("open");
          fitRef.current?.fit();
          sendResize();
          term.focus();
        };
        socket.onmessage = (ev) => {
          // Сервер шлёт сырые байты pty; текстовые кадры тоже допустимы.
          if (ev.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(ev.data));
          } else if (typeof ev.data === "string") {
            term.write(ev.data);
          }
        };
        socket.onclose = () => {
          if (closed) return;
          // Соединение закрылось. Уточняем у сервера статус сессии: если агент
          // завершился (stopped/failed), переподключаться некуда — показываем
          // терминальное состояние "ended". Иначе это обрыв канала — оставляем
          // "closed" с возможностью ручного reconnect.
          setConn("closed");
          void sessionClient
            .get({ sessionId })
            .then((res) => {
              const status = res.session?.status;
              if (
                !closed &&
                (status === SessionStatus.STOPPED ||
                  status === SessionStatus.FAILED)
              ) {
                setConn("ended");
              }
            })
            .catch(() => {
              // Статус не удалось получить — оставляем "closed".
            });
        };
        socket.onerror = () => {
          if (!closed) setConn("error");
        };
      } catch {
        if (!closed) setConn("error");
      }
    })();

    // Ввод пользователя уходит на сервер JSON-кадром {type:"input",data}.
    const dataSub = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data }));
      }
    });

    return () => {
      closed = true;
      dataSub.dispose();
      socket?.close();
      if (wsRef.current === socket) wsRef.current = null;
    };
  }, [sessionId, attempt, sendResize]);

  // Title/right публикуются в под-хедер оболочки (см. SessionHeaderSlot): сам экран
  // возвращает только содержимое — терминал, без внешней шапки.
  useSessionHeader({
    title: <span className="font-mono text-xs">{sessionId}</span>,
    right: (
      <ConnBadge conn={conn} onReconnect={() => setAttempt((a) => a + 1)} />
    ),
  });

  return (
    <div className="relative h-full overflow-hidden bg-[#15191f]">
      {/* overflow-hidden обязателен: скролл должен жить внутри viewport самого
          xterm, а не на этом контейнере. Иначе появление/исчезновение скроллбара
          на контейнере меняет его ширину и запускает петлю ResizeObserver→fit. */}
      <div ref={hostRef} className="absolute inset-0 overflow-hidden p-2" />
      {conn === "connecting" && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      )}
    </div>
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
