import { useEffect, useMemo, useRef } from "react";
import { ExternalLink, Globe, GitPullRequest, Link, Link2 } from "lucide-react";
import type { Preview } from "@/api/gen/brigade/v1/session_pb";
import { FloatingWindow, WindowTitlebar } from "./FloatingWindow";
import { jumpToMessage } from "./jumpToMessage";
import type { LinkKind, SessionLink } from "./links";

/**
 * LinksWindow — плавающее окно «Ссылки»: всё, на что агент сослался в ответах,
 * сгруппировано по сообщению-источнику. Отдельной группой сверху — dev-серверы,
 * зарегистрированные в сессии (brigade preview): они живут не в тексте, а в API.
 *
 * Клик по строке прыгает к сообщению-источнику, клик по подписи открывает адрес.
 */

// Иконка типа ссылки. У внешней — Link, а не ExternalLink: последняя занята кнопкой
// «открыть» справа, и в одной строке две одинаковые иконки читались бы как одно и то же.
const KIND_ICON: Record<LinkKind, typeof Globe> = {
  preview: Globe,
  pr: GitPullRequest,
  external: Link,
};

const KIND_COLOR: Record<LinkKind, string> = {
  preview: "text-success",
  pr: "text-primary",
  external: "text-muted-foreground/70",
};

export function LinksWindow({
  links,
  previews,
  onClose,
}: {
  links: SessionLink[];
  previews: Preview[];
  onClose: () => void;
}) {
  const windowRef = useRef<HTMLDivElement>(null);

  // Справочное окно закрывается по Esc и клику мимо — как всплывающая панель, а не как
  // часть страницы. Чип-переключатель исключён: иначе клик по нему закрывал бы окно
  // «мимо» и тут же открывал заново своим onClick.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null;
      if (windowRef.current?.contains(target ?? null)) return;
      if (target?.closest("[data-dock-chip]")) return;
      onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [onClose]);

  // Группы в порядке ленты: первое упоминание сверху, свежее — ниже.
  const groups = useMemo(() => {
    const bySource = new Map<string, SessionLink[]>();
    for (const l of links) {
      const list = bySource.get(l.sourceId);
      if (list) list.push(l);
      else bySource.set(l.sourceId, [l]);
    }
    return [...bySource.values()];
  }, [links]);

  return (
    <FloatingWindow
      ref={windowRef}
      // Высота по содержимому: при паре ссылок окно не тянет за собой пустоту, при
      // длинном списке упирается в потолок и скроллится внутри.
      className="top-[54px] right-3 z-20 max-h-[min(480px,calc(100%-4.5rem))] w-[340px] max-w-[calc(100%-1.5rem)] lg:right-[66px]"
      titlebar={
        <WindowTitlebar
          icon={Link2}
          title="Ссылки"
          hint="по сообщению"
          onClose={onClose}
        />
      }
    >
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {previews.length > 0 && (
          <section className="mb-2">
            <GroupHeader label="Dev-серверы" count={previews.length} />
            {previews.map((p) => (
              // У dev-сервера нет сообщения-источника — прыгать некуда, вся строка ведёт
              // на сам адрес, вложенных зон клика не возникает.
              <a
                key={p.port}
                href={p.url}
                target="_blank"
                rel="noreferrer"
                className="flex items-start gap-2 rounded-[7px] px-2 py-1.5 transition-colors hover:bg-card"
              >
                <Globe className="mt-0.5 size-3.5 shrink-0 text-success" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[12.5px] text-[#e7e5df]">
                    {p.name || `порт ${p.port}`}
                  </span>
                  <span className="block truncate font-mono text-[10.5px] text-muted-foreground/70">
                    {p.url}
                  </span>
                </span>
              </a>
            ))}
          </section>
        )}

        {groups.map((items) => (
          <section key={items[0].sourceId} className="mb-2">
            <GroupHeader
              label={`Сообщение #${items[0].sourceIndex + 1}`}
              count={items.length}
            />
            {items.map((l) => (
              <LinkRow key={l.url} link={l} />
            ))}
          </section>
        ))}

        {groups.length === 0 && previews.length === 0 && (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">
            Агент пока не присылал ссылок.
          </p>
        )}
      </div>
    </FloatingWindow>
  );
}

function GroupHeader({ label, count }: { label: string; count: number }) {
  return (
    <div className="flex items-center gap-2 px-2 py-1.5">
      <span className="text-[10.5px] tracking-wide text-muted-foreground/60 uppercase">
        {label}
      </span>
      <span className="rounded-full bg-secondary px-1.5 text-[10px] text-muted-foreground">
        {count}
      </span>
    </div>
  );
}

// LinkRow — две РАЗДЕЛЁННЫЕ зоны клика, а не вложенные: само поле строки уводит к
// сообщению-источнику, отдельная иконка справа открывает адрес. Ссылка внутри
// кликабельной строки заставляла бы целиться и гадать, что сейчас произойдёт.
function LinkRow({ link }: { link: SessionLink }) {
  const Icon = KIND_ICON[link.kind];
  return (
    <div className="group/link flex items-stretch gap-1 rounded-[7px] transition-colors hover:bg-card">
      <button
        type="button"
        onClick={() => jumpToMessage(link.sourceId)}
        title="К сообщению-источнику"
        className="flex min-w-0 flex-1 items-start gap-2 px-2 py-1.5 text-left"
      >
        <Icon className={`mt-0.5 size-3.5 shrink-0 ${KIND_COLOR[link.kind]}`} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[12.5px] text-[#e7e5df]">
            {link.label}
          </span>
          <span className="block truncate font-mono text-[10.5px] text-muted-foreground/70">
            {link.url}
          </span>
        </span>
      </button>
      <a
        href={link.url}
        target="_blank"
        rel="noreferrer"
        title="Открыть ссылку"
        className="flex shrink-0 items-center px-2 text-muted-foreground/50 transition-colors hover:text-primary"
      >
        <ExternalLink className="size-3.5" />
      </a>
    </div>
  );
}
