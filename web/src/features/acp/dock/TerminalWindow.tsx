import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { RotateCw } from "lucide-react";
import { TerminalView, type TermConnState } from "@/features/terminal/TerminalView";
import { FloatingWindow } from "./FloatingWindow";

/**
 * TerminalWindow — окно вспомогательного шелла сессии (local — шелл хоста в cwd сессии,
 * docker — exec в контейнер). Настоящее окно: перетаскивается за тайтлбар, тянется за
 * правый нижний угол, положение и размер переживают перезагрузку.
 *
 * Шелл живёт ровно столько, сколько открыто окно: закрытие размонтирует TerminalView и
 * разрывает WS, что завершает процесс на сервере.
 */

type Geometry = { x: number; y: number; w: number; h: number };

const GEOMETRY_KEY = "brigade.dock.terminal-geometry";
const DEFAULT_W = 620;
const DEFAULT_H = 300;
const MIN_W = 320;
const MIN_H = 160;

function loadGeometry(): Geometry | null {
  try {
    const raw = localStorage.getItem(GEOMETRY_KEY);
    if (!raw) return null;
    const g = JSON.parse(raw) as Partial<Geometry>;
    if ([g.x, g.y, g.w, g.h].some((v) => typeof v !== "number")) return null;
    return g as Geometry;
  } catch {
    return null;
  }
}

const clamp = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), max);

export function TerminalWindow({
  sessionId,
  onClose,
}: {
  sessionId: string;
  onClose: () => void;
}) {
  const [conn, setConn] = useState<TermConnState>("connecting");
  // Счётчик попыток переподключения: инкремент пересоздаёт WS (и шелл на сервере).
  const [attempt, setAttempt] = useState(0);
  const [geometry, setGeometry] = useState<Geometry | null>(loadGeometry);
  const windowRef = useRef<HTMLDivElement>(null);

  // Размер области, в которой окно живёт (позиционируется от неё же — absolute внутри
  // контейнера страницы сессии). Нужен и для первичной раскладки, и для клампа.
  const areaSize = useCallback(() => {
    const parent = windowRef.current?.offsetParent as HTMLElement | null;
    return {
      w: parent?.clientWidth ?? window.innerWidth,
      h: parent?.clientHeight ?? window.innerHeight,
    };
  }, []);

  // Первое открытие: кладём окно в правый нижний угол области — там же, где оно было
  // прибито до того, как стало перетаскиваемым.
  useLayoutEffect(() => {
    if (geometry) return;
    const area = areaSize();
    const w = Math.min(DEFAULT_W, area.w - 24);
    const h = Math.min(DEFAULT_H, area.h - 24);
    setGeometry({
      w,
      h,
      x: Math.max(12, area.w - w - 66),
      y: Math.max(12, area.h - h - 20),
    });
  }, [geometry, areaSize]);

  useEffect(() => {
    if (!geometry) return;
    try {
      localStorage.setItem(GEOMETRY_KEY, JSON.stringify(geometry));
    } catch {
      // Приватный режим/переполнение — геометрия просто не переживёт перезагрузку.
    }
  }, [geometry]);

  // Сжали окно браузера — терминал мог оказаться за краем области сессии. Сначала
  // вжимаем размер, потом позицию: иначе окно шире области упёрлось бы в x=0 и всё
  // равно торчало правым краем.
  useEffect(() => {
    const onResize = () => {
      const area = areaSize();
      setGeometry((g) => {
        if (!g) return g;
        const w = clamp(g.w, MIN_W, area.w);
        const h = clamp(g.h, MIN_H, area.h);
        return { w, h, x: clamp(g.x, 0, area.w - w), y: clamp(g.y, 0, area.h - h) };
      });
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [areaSize]);

  // drag/resize через один обработчик: обе операции — это «зажали, ведём, отпустили»,
  // отличается лишь то, что считается из смещения. Pointer capture, поэтому курсор может
  // уходить за пределы окна — жест не рвётся.
  //
  // Терминал принадлежит конкретной сессии, поэтому и живёт строго внутри её области:
  // клампим так, чтобы окно целиком оставалось в границах, а не торчало за краем.
  const startGesture = (
    e: React.PointerEvent<HTMLElement>,
    apply: (dx: number, dy: number, area: { w: number; h: number }) => Geometry,
  ) => {
    if (e.button !== 0 || !geometry) return;
    // Без preventDefault браузер по тому же нажатию начинает выделение текста и тянет
    // его по всей странице вслед за курсором. user-select на body — страховка на время
    // жеста: выделение могло начаться и до захвата указателя.
    e.preventDefault();
    document.body.style.userSelect = "none";

    const handle = e.currentTarget;
    const area = areaSize();
    const from = { x: e.clientX, y: e.clientY };
    handle.setPointerCapture(e.pointerId);

    const onMove = (ev: PointerEvent) =>
      setGeometry(apply(ev.clientX - from.x, ev.clientY - from.y, area));
    const onUp = () => {
      document.body.style.userSelect = "";
      handle.releasePointerCapture(e.pointerId);
      handle.removeEventListener("pointermove", onMove);
    };
    handle.addEventListener("pointermove", onMove);
    handle.addEventListener("pointerup", onUp, { once: true });
    handle.addEventListener("pointercancel", onUp, { once: true });
  };

  const startDrag = (e: React.PointerEvent<HTMLElement>) => {
    const start = geometry;
    if (!start) return;
    startGesture(e, (dx, dy, area) => ({
      ...start,
      x: clamp(start.x + dx, 0, area.w - start.w),
      y: clamp(start.y + dy, 0, area.h - start.h),
    }));
  };

  const startResize = (e: React.PointerEvent<HTMLElement>) => {
    const start = geometry;
    if (!start) return;
    e.stopPropagation();
    startGesture(e, (dx, dy, area) => ({
      ...start,
      w: clamp(start.w + dx, MIN_W, area.w - start.x),
      h: clamp(start.h + dy, MIN_H, area.h - start.y),
    }));
  };

  return (
    <FloatingWindow
      ref={windowRef}
      style={
        geometry
          ? {
              left: geometry.x,
              top: geometry.y,
              width: geometry.w,
              height: geometry.h,
              minWidth: MIN_W,
              minHeight: MIN_H,
            }
          : { visibility: "hidden" }
      }
      // z-40 — выше чипов управления окнами (z-30): терминал двигают куда угодно, в том
      // числе под чипы, и подныривать под них перетаскиваемое окно не должно.
      className="z-40 overflow-hidden rounded-[12px]"
      titlebar={
        <div
          onPointerDown={startDrag}
          // touch-action: none — иначе на трекпаде/тач-экране жест уходит в прокрутку
          // страницы вместо перетаскивания.
          className="relative flex h-[34px] shrink-0 cursor-grab touch-none items-center gap-2 border-b border-border bg-background px-3 active:cursor-grabbing"
        >
          {/* Заголовок центрируем по всему тайтлбару, а не по остатку строки: в потоке
              его сдвигал бы вправо светофор слева. pointer-events-none — чтобы подпись
              не отбирала у тайтлбара перетаскивание. */}
          <span className="pointer-events-none absolute inset-0 flex items-center justify-center px-16 text-xs text-muted-foreground select-none">
            <span className="truncate">Терминал — {sessionId.slice(0, 8)}</span>
          </span>
          {/* Светофор в стиле macOS: активна только красная точка — закрывает окно. */}
          <span className="flex gap-1.5">
            <button
              type="button"
              onClick={onClose}
              onPointerDown={(e) => e.stopPropagation()}
              aria-label="Закрыть терминал"
              className="size-[11px] rounded-full bg-destructive"
            />
            <span className="size-[11px] rounded-full bg-warning/70" />
            <span className="size-[11px] rounded-full bg-success/70" />
          </span>
          <span className="flex-1" />
          {(conn === "closed" || conn === "error") && (
            <button
              type="button"
              onClick={() => {
                setConn("connecting");
                setAttempt((a) => a + 1);
              }}
              onPointerDown={(e) => e.stopPropagation()}
              className="flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              <RotateCw className="size-3" />
              переподключить
            </button>
          )}
        </div>
      }
    >
      <TerminalView
        kind="shell"
        sessionId={sessionId}
        attempt={attempt}
        onConnChange={setConn}
      />
      {/* Своя ручка вместо CSS resize: нативная рисуется системой поверх тёмного тела
          терминала и там попросту не читается — непонятно, что окно вообще тянется. */}
      <div
        onPointerDown={startResize}
        title="Потянуть, чтобы изменить размер"
        className="absolute right-0 bottom-0 z-10 flex size-5 cursor-nwse-resize touch-none items-end justify-end p-1 text-muted-foreground/60 transition-colors hover:text-foreground"
      >
        <svg viewBox="0 0 10 10" className="size-2.5" aria-hidden>
          <path
            d="M9.5 1.5 1.5 9.5M9.5 5.5 5.5 9.5"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            fill="none"
          />
        </svg>
      </div>
    </FloatingWindow>
  );
}
