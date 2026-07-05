import { useCallback, useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { streamUrl } from "@/api/ws";

// Состояние WS-соединения терминала. Семантика "сессия завершена" (ended) —
// забота вызывающего экрана: TerminalView сообщает только о самом соединении.
export type TermConnState = "connecting" | "open" | "closed" | "error";

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

/**
 * TerminalView — xterm.js поверх WS-стрима brigade: терминал агента CLI-сессии
 * (kind="terminal") либо вспомогательный шелл рядом с сессией (kind="shell").
 *
 * Протокол один: сервер шлёт raw-байты pty бинарными кадрами, клиент — JSON
 * {type:"input",data} | {type:"resize",cols,rows}. Инкремент attempt переоткрывает
 * соединение (ручной reconnect); о смене состояния сообщает onConnChange.
 */
export function TerminalView({
  kind,
  sessionId,
  attempt = 0,
  onConnChange,
}: {
  kind: "terminal" | "shell";
  sessionId: string;
  attempt?: number;
  onConnChange?: (conn: TermConnState) => void;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [conn, setConnState] = useState<TermConnState>("connecting");
  // onConnChange не входит в зависимости эффектов: колбэк родителя может меняться
  // каждый рендер, а пересоздание терминала/соединения из-за этого недопустимо.
  const onConnRef = useRef(onConnChange);
  onConnRef.current = onConnChange;

  const setConn = useCallback((c: TermConnState) => {
    setConnState(c);
    onConnRef.current?.(c);
  }, []);

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

    // Копирование/вставка: xterm выделяет текст мышью (в mouse-режиме приложения — с
    // зажатым Shift), но сам в буфер обмена не кладёт. Копируем выделение автоматически
    // при его изменении (copy-on-select, как в терминалах Linux) и по Ctrl/Cmd+Shift+C;
    // вставка — Ctrl/Cmd+Shift+V (шлём как ввод). Без этого нельзя, например, скопировать
    // URL из `claude login`.
    const copySelection = () => {
      const sel = term.getSelection();
      if (sel) void navigator.clipboard?.writeText(sel).catch(() => {});
    };
    const selSub = term.onSelectionChange(copySelection);
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.shiftKey && (e.key === "c" || e.key === "C")) {
        copySelection();
        return false;
      }
      if (mod && e.shiftKey && (e.key === "v" || e.key === "V")) {
        void navigator.clipboard
          ?.readText()
          .then((text) => {
            if (text) {
              wsRef.current?.send(JSON.stringify({ type: "input", data: text }));
            }
          })
          .catch(() => {});
        return false;
      }
      return true;
    });

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
      selSub.dispose();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [sendResize]);

  // Подключение к WS: тикет → URL → бинарь в терминал, ввод JSON-ом.
  useEffect(() => {
    const term = termRef.current;
    if (!term) return;

    let closed = false;
    let socket: WebSocket | null = null;
    setConn("connecting");

    void (async () => {
      try {
        const url = await streamUrl(kind, sessionId);
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
          if (!closed) setConn("closed");
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
  }, [kind, sessionId, attempt, sendResize, setConn]);

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
