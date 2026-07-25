import { useEffect, useMemo, useState } from "react";
import { useAuiState } from "@assistant-ui/react";
import { messagePlainText } from "./links";
import { jumpToMessage } from "./jumpToMessage";

/**
 * MsgNav — вертикальная шкала навигации по ленте («Time Machine»): по пилюле на серию
 * реплик, прижаты к правому краю чата и центрированы по высоте. Шкала непрерывно следует
 * за прокруткой: положение в ленте даёт ДРОБНЫЙ фокус, вокруг которого пилюли плавно
 * растут и светлеют, а вдали — мельчают и сгущаются. Клик прыгает к серии, ховер даёт
 * магнификацию соседей (как док macOS) и тултип с началом реплики.
 *
 * Высота шкалы не зависит ни от фокуса, ни от ховера: строки делят фиксированную высоту
 * контейнера через flex-grow, а пилюля центрируется внутри своей строки. Иначе рост пилюль
 * растягивал бы центрированный блок и шкала дёргалась бы под курсором.
 */

// Магнификация: гауссово затухание от наведённой пилюли к соседям.
function magnify(index: number, hover: number): number {
  if (hover < 0) return 0;
  return Math.exp(-((index - hover) ** 2) / 1.6);
}

// Длина подписи в тултипе: дальше обрезаем — шкала не место для полного сообщения.
const LABEL_MAX = 70;

// Идеальный шаг одной пилюли по вертикали. Реальный шаг получается делением высоты
// контейнера, поэтому на длинной сессии шкала не вылезает за экран, а на короткой не
// растягивается на всю высоту.
const STEP = 23;

export function MsgNav() {
  const messages = useAuiState((s) => s.thread.messages);
  const [hover, setHover] = useState(-1);

  // Пилюля — не сообщение, а СЕРИЯ подряд идущих реплик одной стороны, с якорем на первой
  // из них. На один запрос агент выдаёт десяток сообщений (текст, вызовы инструментов,
  // системные уведомления), и пилюля на каждое превращала бы шкалу в сплошную гребёнку,
  // где все пилюли ведут в одно и то же место ленты.
  const pills = useMemo(() => {
    const series: { id: string; isUser: boolean; text: string }[] = [];
    for (const m of messages) {
      const isUser = m.role === "user";
      const text = messagePlainText(m);
      const last = series[series.length - 1];
      if (last && last.isUser === isUser) {
        // Первое непустое: серия часто начинается сообщением из одних tool-call'ов.
        if (!last.text) last.text = text;
        continue;
      }
      series.push({ id: m.id, isUser, text });
    }
    return series.map((s) => {
      const who = s.isUser ? "Вы" : "Агент";
      const text =
        s.text.length > LABEL_MAX ? `${s.text.slice(0, LABEL_MAX)}…` : s.text;
      return {
        id: s.id,
        isUser: s.isUser,
        label: text ? `${who} · ${text}` : who,
      };
    });
  }, [messages]);

  const n = pills.length;

  // focus — ДРОБНАЯ позиция прокрутки в шкале серий (2.4 = чуть ниже начала третьей).
  // Считаем сами по якорям в DOM: у рантайма нет события «докуда доскроллено», а плавность
  // возможна только на непрерывной величине — индекс видимого сообщения давал бы рывки.
  const [focus, setFocus] = useState(n - 1);
  useEffect(() => {
    if (n === 0) return;
    const viewport = document.querySelector<HTMLElement>(
      '[data-slot="aui_thread-viewport"]',
    );
    if (!viewport) return;

    let frame = 0;
    const measure = () => {
      frame = 0;
      const box = viewport.getBoundingClientRect();
      // Линия фокуса — треть высоты вьюпорта. По верхней кромке текущим оказывалось бы
      // длинное сообщение, уже ушедшее с экрана; по центру фокус запаздывал бы.
      const line = box.top + box.height / 3;
      const tops = pills.map(
        (p) =>
          document
            .querySelector<HTMLElement>(`[data-nav-id="${CSS.escape(p.id)}"]`)
            ?.getBoundingClientRect().top ?? Number.NaN,
      );

      let next = n - 1;
      if (Number.isNaN(tops[0]) || line <= tops[0]) {
        next = 0;
      } else {
        for (let i = 0; i < n - 1; i++) {
          const span = tops[i + 1] - tops[i];
          if (line < tops[i + 1]) {
            // Доля пройденного расстояния между соседними якорями и есть дробная часть.
            next = span > 0 ? i + (line - tops[i]) / span : i;
            break;
          }
        }
      }
      setFocus(next);
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };

    measure();
    viewport.addEventListener("scroll", onScroll, { passive: true });
    // Лента меняет высоту и без прокрутки (стриминг ответа, раскрытие карточки) — фокус
    // при этом уезжает, поэтому пересчитываем ещё и по изменению размеров содержимого.
    const resize = new ResizeObserver(onScroll);
    resize.observe(viewport);
    return () => {
      viewport.removeEventListener("scroll", onScroll);
      resize.disconnect();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [pills, n]);

  if (n === 0) return null;

  const cur = Math.min(Math.max(focus, 0), n - 1);
  // Возраст пилюли — близость к фокусу. Когда лента прокручена в конец (cur = n-1),
  // выражение вырождается в исходное «старое сверху мелкое, сейчас снизу крупное».
  const span = Math.max(cur, n - 1 - cur, 1);
  const ageAt = (i: number) => 1 - Math.abs(i - cur) / span;

  return (
    <div
      // На узких экранах шкалу прячем: она встала бы поверх композера, а места для
      // магнификации и тултипа всё равно нет.
      className="pointer-events-none absolute inset-y-0 right-[18px] z-10 hidden w-[46px] animate-[rail-in_0.35s_ease] flex-col justify-center py-4 lg:flex"
      onMouseLeave={() => setHover(-1)}
    >
      <div
        // Высота задана здесь и только здесь: строки внутри делят её flex-grow'ом, поэтому
        // ни магнификация, ни смена фокуса не меняют габарит блока.
        style={{ height: n * STEP }}
        className="flex max-h-full flex-col items-end"
      >
        {pills.map((p, i) => {
          const t = ageAt(i);
          // proximity — непрерывная близость к фокусу: даёт активной пилюле рост без
          // скачка при переходе фокуса с одной серии на другую.
          const proximity = Math.max(0, 1 - Math.abs(i - cur));
          const active = Math.abs(i - cur) < 0.5;
          const s = magnify(i, hover);
          const width = Math.round(14 + t * 14 + s * 26);
          const height = Math.round(4 + proximity * 2 + s * 4);
          const opacity = Math.min(1, 0.4 + t * 0.5 + s * 0.6);
          const alpha = p.isUser ? 0.65 + s * 0.35 : 0.5 + s * 0.5;
          const background = active
            ? "linear-gradient(180deg,#e08464,#c96442)"
            : p.isUser
              ? `linear-gradient(180deg,rgba(224,132,100,${alpha}),rgba(201,100,66,${alpha}))`
              : `linear-gradient(180deg,rgba(160,157,148,${alpha}),rgba(110,107,99,${alpha}))`;

          return (
            <div
              key={p.id}
              onMouseEnter={() => setHover(i)}
              onClick={() => jumpToMessage(p.id)}
              // Строки идут вплотную и делят высоту пропорционально близости к фокусу:
              // у фокуса просторнее, вдали гуще. Мёртвых зон между пилюлями нет — вся
              // строка кликабельна, поэтому ховер перетекает.
              style={{ flexGrow: 1 + t, flexBasis: 0 }}
              className="pointer-events-auto relative flex min-h-0 w-full cursor-pointer items-center justify-end pl-6"
            >
              {hover === i && (
                <div className="pointer-events-none absolute top-1/2 right-[52px] z-10 animate-[tip-in_0.18s_cubic-bezier(0.2,0.8,0.2,1)] rounded-[9px] border border-white/8 bg-[rgba(28,27,26,0.82)] px-3 py-[7px] text-[11.5px] whitespace-nowrap text-[#f0efe9] shadow-[0_12px_32px_rgba(0,0,0,0.5)] backdrop-blur-[14px] backdrop-saturate-150">
                  {p.label}
                </div>
              )}
              <div
                style={{
                  width,
                  height,
                  opacity,
                  background,
                  // Переход по размеру нужен только магнификации под курсором. От
                  // прокрутки размеры и так меняются покадрово — там transition давал бы
                  // запаздывание вместо непрерывного отклика.
                  transition:
                    hover >= 0
                      ? "width .26s cubic-bezier(.25,1,.4,1), height .26s cubic-bezier(.25,1,.4,1), opacity .26s ease, background .26s ease"
                      : "background .26s ease",
                }}
                className={
                  active
                    ? "shrink-0 animate-[now-glow_2.6s_ease-in-out_infinite] rounded-full"
                    : "shrink-0 rounded-full"
                }
              />
            </div>
          );
        })}
      </div>
      <div className="mt-3 pr-0.5 text-[9px] tracking-[0.1em] text-[#6c695f] uppercase">
        сейчас
      </div>
    </div>
  );
}
