import {
  Fragment,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  Link,
  Outlet,
  useLocation,
  useNavigate,
  useParams,
} from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import {
  Archive,
  CircleArrowUp,
  Loader2,
  LogOut,
  MessagesSquare,
  NotebookPen,
  Pencil,
  Plus,
  RefreshCw,
  Settings,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { sessionClient } from "@/api/client";
import {
  Session,
  SessionKind,
} from "@/api/gen/brigade/v1/session_pb";
import { useAuth } from "@/features/auth/AuthContext";
import { kindLabel, sessionRoute } from "@/lib/term/format";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { ShellPanel } from "@/features/terminal/ShellPanel";
import { CreateSessionDialog } from "./CreateSessionDialog";
import { EnvironmentSwitcher } from "@/features/desktop/EnvironmentSwitcher";
import {
  SessionHeaderProvider,
  useSessionHeaderSlot,
} from "./SessionHeaderSlot";

type LoadState = "loading" | "ready" | "error";

// Контекст оболочки: открытие диалога создания сессии доступно из пустого состояния
// (index-роут) тем же триггером, что и в шапке sidebar.
type SessionShellValue = {
  openCreate: () => void;
};

const SessionShellContext = createContext<SessionShellValue | null>(null);

export function useSessionShell() {
  const ctx = useContext(SessionShellContext);
  if (!ctx) {
    throw new Error("useSessionShell must be used within SessionLayout");
  }
  return ctx;
}

/**
 * SessionLayout — постоянная оболочка всех экранов после логина: складываемый
 * sidebar со списком сессий слева и областью контента (SidebarInset) справа.
 * Список сессий живёт здесь и переключается кликом без кнопки «назад»; активная
 * сессия определяется по :sessionId из URL. Контент рендерится через <Outlet/>:
 * index — пустое состояние, /s/:sessionId — конкретная сессия (см. App.tsx).
 */
export function SessionLayout() {
  const navigate = useNavigate();
  const { desktop } = useAuth();
  // activeId — сессия, открытая сейчас в области контента (/s/:sessionId). Нужна, чтобы
  // удаление текущей сессии увело с её (теперь несуществующего) маршрута на пустой экран.
  const location = useLocation();
  // activeId — открытая ЖИВАЯ сессия (роут /s/:id): к ней относятся topbar, вспом.
  // терминал и оверлеи. На /archive/:id тот же параметр sessionId есть, но это
  // readonly-просмотр архива — там ни topbar'а, ни терминала быть не должно.
  const { sessionId: routeSessionId } = useParams<{ sessionId: string }>();
  const activeId = location.pathname.startsWith("/archive")
    ? undefined
    : routeSessionId;
  const [sessions, setSessions] = useState<Session[]>([]);
  const [state, setState] = useState<LoadState>("loading");
  const [createOpen, setCreateOpen] = useState(false);
  // deletingIds — сессии, удаление которых сейчас выполняется на сервере. Teardown
  // контейнера/процесса занимает до ~15 секунд, и без индикации
  // клик выглядит проигнорированным (а повторные клики порождали параллельные
  // удаления). Блокируется только сама удаляемая сессия (пункт списка + её контент,
  // если она открыта) — остальной UI живёт, можно перейти к другой сессии.
  const [deletingIds, setDeletingIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  // archivingIds — сессии, архивация которых сейчас идёт. Как deletingIds: архивация
  // делает recap-turn (несколько секунд) и останавливает контейнер, поэтому её надо
  // показать блокирующим оверлеем открытой сессии, а не молчаливым фоном.
  const [archivingIds, setArchivingIds] = useState<ReadonlySet<string>>(
    new Set(),
  );

  const load = useCallback(async (silent = false) => {
    if (!silent) setState("loading");
    try {
      const res = await sessionClient.list({});
      // Новые сверху: бэкенд не гарантирует порядок, сортируем по created_at.
      const sorted = [...res.sessions].sort((a, b) =>
        Number(b.createdAt - a.createdAt),
      );
      setSessions(sorted);
      setState("ready");
    } catch {
      if (!silent) setState("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load(true);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    if (!activeId) return;
    return () => { void sessionClient.markRead({ sessionId: activeId }); };
  }, [activeId]);

  useEffect(() => {
    if (!activeId || !sessions.some((session) => session.id === activeId && session.unread)) return;
    setSessions((prev) => prev.map((session) => {
      if (session.id !== activeId || !session.unread) return session;
      const copy = session.clone();
      copy.unread = false;
      return copy;
    }));
    void sessionClient.markRead({ sessionId: activeId });
  }, [activeId, sessions]);

  useEffect(() => {
    const onSessionTitle = (event: Event) => {
      const { sessionId, title } = (event as CustomEvent<{ sessionId: string; title: string }>).detail;
      setSessions((prev) => prev.map((session) =>
        session.id === sessionId && !session.name
          ? withName(session, sessionId, title)
          : session,
      ));
    };
    window.addEventListener("brigade:session-title", onSessionTitle);
    return () => window.removeEventListener("brigade:session-title", onSessionTitle);
  }, []);

  const onDelete = useCallback(
    async (id: string) => {
      setDeletingIds((prev) => new Set(prev).add(id));
      try {
        await sessionClient.delete({ sessionId: id });
        setSessions((prev) => prev.filter((s) => s.id !== id));
        toast.success("Сессия удалена");
        // Удалили открытую сейчас сессию — её маршрут /s/:id больше не существует,
        // уводим на пустой экран. Сверяемся с актуальным location, а не с замыканием:
        // пока шло удаление, пользователь мог перейти к другой сессии — уводить его
        // с неё нельзя.
        if (window.location.pathname.endsWith(`/${id}`)) {
          navigate("/sessions");
        }
      } catch (err) {
        toast.error(
          err instanceof ConnectError
            ? err.rawMessage
            : "Не удалось удалить сессию",
        );
      } finally {
        setDeletingIds((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
      }
    },
    [navigate],
  );

  const onRename = useCallback(
    async (id: string, name: string) => {
      // Оптимистично обновляем подпись; при ошибке откатываемся к данным с сервера.
      const prevName = sessions.find((s) => s.id === id)?.name ?? "";
      setSessions((prev) => prev.map((s) => withName(s, id, name)));
      try {
        await sessionClient.update({ sessionId: id, name });
      } catch (err) {
        setSessions((prev) => prev.map((s) => withName(s, id, prevName)));
        toast.error(
          err instanceof ConnectError
            ? err.rawMessage
            : "Не удалось переименовать сессию",
        );
      }
    },
    [sessions],
  );

  const onArchive = useCallback(
    async (id: string) => {
      // Архивация зовёт агента за recap (несколько секунд) и останавливает контейнер —
      // как удаление, показываем блокирующий оверлей открытой сессии (archivingIds),
      // иначе recap-turn лишь мигает индикатором «агент в фоне» и сессия молча исчезает.
      setArchivingIds((prev) => new Set(prev).add(id));
      try {
        await sessionClient.archive({ sessionId: id });
        setSessions((prev) => prev.filter((s) => s.id !== id));
        toast.success("Сессия в архиве");
        // Уводим на страницу архива — там уже есть карточка с пересказом.
        if (window.location.pathname.endsWith(`/${id}`)) {
          navigate("/archive");
        }
      } catch (err) {
        toast.error(
          err instanceof ConnectError
            ? err.rawMessage
            : "Не удалось архивировать сессию",
        );
      } finally {
        setArchivingIds((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
      }
    },
    [navigate],
  );

  // reloadingId — сессия, чей ACP-агент сейчас перезапускается на актуальном окружении.
  const [reloadingId, setReloadingId] = useState<string | null>(null);
  const onReloadAgent = useCallback(async (id: string) => {
    setReloadingId(id);
    try {
      await sessionClient.reloadAgent({ sessionId: id });
      toast.success("Агент перезапущен на актуальном окружении");
      if (window.location.pathname.endsWith(`/${id}`)) {
        window.location.reload();
      } else {
        await load(true);
      }
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось перезагрузить агента",
      );
    } finally {
      setReloadingId(null);
    }
  }, [load]);

  const openCreate = useCallback(() => setCreateOpen(true), []);

  // acpActive — открытая сессия работает в ACP-режиме: её вспомогательный терминал
  // рисует SessionDock, а не докнутая полоса снизу.
  const acpActive =
    activeId !== undefined &&
    sessions.find((s) => s.id === activeId)?.kind === SessionKind.ACP;

  // Первая (самая новая) сессия определяет положение группы, внутри порядок тоже остаётся
  // от новых к старым. Сессии без подписи остаются самостоятельными строками.
  const groups = useMemo(() => {
    const out: { label: string; sessions: Session[] }[] = [];
    const byLabel = new Map<string, Session[]>();
    for (const s of sessions) {
      if (!s.groupLabel) {
        out.push({ label: "", sessions: [s] });
        continue;
      }
      const existing = byLabel.get(s.groupLabel);
      if (existing) {
        existing.push(s);
        continue;
      }
      const grouped = [s];
      byLabel.set(s.groupLabel, grouped);
      out.push({ label: s.groupLabel, sessions: grouped });
    }
    return out;
  }, [sessions]);

  return (
    <SessionShellContext.Provider value={{ openCreate }}>
      <SessionHeaderProvider>
        {/* 260px — ширина рейла по макету (дефолт shadcn 16rem/256px). */}
        <SidebarProvider style={{ "--sidebar-width": "260px" } as CSSProperties}>
          <SidebarAutoClose />
          <Sidebar collapsible="icon">
            <SidebarHeader>
              <div className="flex items-center justify-between gap-2 group-data-[collapsible=icon]:justify-center">
                <Link
                  to="/sessions"
                  className="flex shrink-0 items-center gap-2 px-1 font-semibold"
                >
                  <img
                    src="/logo.svg"
                    alt=""
                    className="size-[26px] shrink-0 rounded-[7px]"
                  />
                  <span className="group-data-[collapsible=icon]:hidden">
                    brigade
                  </span>
                </Link>
                <SidebarTrigger className="group-data-[collapsible=icon]:hidden" />
              </div>
              {desktop && <EnvironmentSwitcher />}
              <Button
                onClick={openCreate}
                className="h-auto justify-start gap-2 rounded-[9px] py-[9px] text-[13px] font-medium group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0"
              >
                <Plus className="size-4 shrink-0" />
                <span className="group-data-[collapsible=icon]:hidden">
                  Новая сессия
                </span>
              </Button>
            </SidebarHeader>

            {/* Колонка не скроллится целиком: прокручивается только список сессий, а
                «Заметки»/«Архив» и профиль остаются на виду при любой высоте окна. */}
            <SidebarContent className="overflow-hidden">
              <SidebarGroup className="min-h-0 flex-1 gap-0 pb-0">
                {/* Заголовок группы: сам никуда не ведёт, поэтому без hover-подсветки и
                    курсора — иначе выглядел бы кликабельным пунктом меню. Кликается только
                    иконка обновления справа. */}
                <div className="flex h-8 items-center gap-2 px-2 text-[13px] group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
                  <MessagesSquare className="size-4 shrink-0" />
                  <span className="flex-1 group-data-[collapsible=icon]:hidden">
                    Сессии
                  </span>
                  <span className="inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-secondary px-1.5 text-[10.5px] text-muted-foreground group-data-[collapsible=icon]:hidden">
                    {sessions.length}
                  </span>
                  <button
                    type="button"
                    onClick={() => void load()}
                    disabled={state === "loading"}
                    aria-label="Обновить список сессий"
                    title="Обновить список сессий"
                    className="flex size-5 items-center justify-center rounded text-sidebar-foreground/50 transition-colors hover:text-sidebar-foreground disabled:opacity-50 group-data-[collapsible=icon]:hidden"
                  >
                    <RefreshCw
                      className={
                        state === "loading"
                          ? "size-3.5 animate-spin"
                          : "size-3.5"
                      }
                    />
                  </button>
                </div>
                {/* Список забирает всю свободную высоту и скроллится внутри себя. Жёсткого
                    потолка нет намеренно: с ним при высоком окне оставалась дыра, а при
                    низком нижние разделы выдавливало за край колонки. */}
                <SidebarGroupContent className="mt-1.5 min-h-0 flex-1 overflow-y-auto">
                  <SidebarMenu>
                    {state === "loading" &&
                      Array.from({ length: 5 }).map((_, i) => (
                        <SidebarMenuItem key={i}>
                          <SidebarMenuSkeleton showIcon />
                        </SidebarMenuItem>
                      ))}

                    {state === "error" && (
                      <div className="px-2 py-3 text-xs text-sidebar-foreground/60">
                        Не удалось загрузить список.{" "}
                        <button
                          type="button"
                          onClick={() => void load()}
                          className="underline underline-offset-2 hover:text-sidebar-foreground"
                        >
                          Повторить
                        </button>
                      </div>
                    )}

                    {state === "ready" && sessions.length === 0 && (
                      <div className="px-2 py-3 text-xs text-sidebar-foreground/60 group-data-[collapsible=icon]:hidden">
                        Пока нет сессий.
                      </div>
                    )}

                    {state === "ready" && groups.map((group) => (
                      <Fragment key={group.label || group.sessions[0].id}>
                        {group.label && (
                          <SidebarMenuItem>
                            <div className="mx-1 mt-1 flex h-7 items-center gap-2 rounded-[8px] bg-sidebar-accent/60 px-2 text-[12px] font-medium text-sidebar-foreground/75 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
                              <MessagesSquare className="size-3.5 shrink-0" />
                              <span className="min-w-0 flex-1 truncate group-data-[collapsible=icon]:hidden">{group.label}</span>
                              <span className="text-[10px] tabular-nums text-sidebar-foreground/45 group-data-[collapsible=icon]:hidden">{group.sessions.length}</span>
                            </div>
                          </SidebarMenuItem>
                        )}
                        {group.sessions.map((s) => (
                          <SessionItem
                            key={s.id}
                            session={s}
                            grouped={Boolean(group.label)}
                            busy={
                              deletingIds.has(s.id) ||
                              archivingIds.has(s.id) ||
                              reloadingId === s.id
                            }
                            deleting={deletingIds.has(s.id)}
                            archiving={archivingIds.has(s.id)}
                            reloading={reloadingId === s.id}
                            onOpen={() => navigate(sessionRoute(s.id))}
                            onDelete={() => void onDelete(s.id)}
                            onRename={(name) => void onRename(s.id, name)}
                            onArchive={() => void onArchive(s.id)}
                            onReloadAgent={() => void onReloadAgent(s.id)}
                          />
                        ))}
                      </Fragment>
                    ))}
                  </SidebarMenu>
                </SidebarGroupContent>
              </SidebarGroup>

              {/* Разделитель отделяет список сессий от постоянных разделов; над профилем
                  внизу разделителя по макету нет. */}
              <div className="mx-3 mt-1.5 h-px shrink-0 bg-sidebar-border" />
              <SidebarGroup className="shrink-0 py-2">
                <SidebarMenu>
                  <SidebarMenuItem>
                    <SidebarMenuButton
                      onClick={() => navigate("/memory")}
                      isActive={location.pathname.startsWith("/memory")}
                      tooltip="Заметки"
                      className="rounded-[8px] text-[13px] text-sidebar-foreground/70"
                    >
                      <NotebookPen className="size-4" />
                      Заметки
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                  <SidebarMenuItem>
                    <SidebarMenuButton
                      onClick={() => navigate("/archive")}
                      isActive={location.pathname.startsWith("/archive")}
                      tooltip="Архив"
                      className="rounded-[8px] text-[13px] text-sidebar-foreground/70"
                    >
                      <Archive className="size-4" />
                      Архив
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                </SidebarMenu>
              </SidebarGroup>
            </SidebarContent>

            {!desktop && (
              <SidebarFooter>
                <UserMenu />
              </SidebarFooter>
            )}
            <SidebarRail />
          </Sidebar>

          <SidebarInset className="h-svh min-h-0">
            <SessionTopbar />
            <div className="relative flex min-h-0 flex-1 flex-col">
              <div className="min-h-0 flex-1">
                <Outlet />
              </div>
              {/* Вспомогательный шелл активной CLI-сессии. key пересоздаёт панель при
                  переключении сессии: шелл принадлежит конкретной сессии. У ACP-сессий
                  терминал живёт плавающим окном в SessionDock — докнутая полоса дала бы
                  второй шелл рядом с ним. */}
              {activeId && !acpActive && (
                <ShellPanel key={activeId} sessionId={activeId} />
              )}
              {/* Оверлей контента открытой сессии на время её удаления: блокируется
                  только эта сессия, сайдбар доступен — можно перейти к другой. */}
              {activeId && deletingIds.has(activeId) && (
                <div className="bg-background/60 absolute inset-0 z-40 flex items-center justify-center backdrop-blur-sm">
                  <div className="bg-background flex items-center gap-3 rounded-lg border px-5 py-4 shadow-lg">
                    <Loader2 className="text-muted-foreground size-5 animate-spin" />
                    <div className="text-sm">
                      <div className="font-medium">Сессия удаляется…</div>
                      <div className="text-muted-foreground text-xs">
                        Останавливаем агента и освобождаем ресурсы.
                      </div>
                    </div>
                  </div>
                </div>
              )}
              {activeId && archivingIds.has(activeId) && (
                <div className="bg-background/60 absolute inset-0 z-40 flex items-center justify-center backdrop-blur-sm">
                  <div className="bg-background flex items-center gap-3 rounded-lg border px-5 py-4 shadow-lg">
                    <Loader2 className="text-muted-foreground size-5 animate-spin" />
                    <div className="text-sm">
                      <div className="font-medium">Сессия архивируется…</div>
                      <div className="text-muted-foreground text-xs">
                        Агент готовит пересказ, сохраняем историю и
                        останавливаем контейнер.
                      </div>
                    </div>
                  </div>
                </div>
              )}
              {activeId && reloadingId === activeId && (
                <div className="bg-background/60 absolute inset-0 z-40 flex items-center justify-center backdrop-blur-sm">
                  <div className="bg-background flex items-center gap-3 rounded-lg border px-5 py-4 shadow-lg">
                    <Loader2 className="text-muted-foreground size-5 animate-spin" />
                    <div className="text-sm">
                      <div className="font-medium">Агент перезапускается…</div>
                      <div className="text-muted-foreground text-xs">
                        Обновляем окружение и восстанавливаем сессию.
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </div>
          </SidebarInset>
        </SidebarProvider>

        <CreateSessionDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onCreated={(s) => {
            // Оптимистично добавляем созданную сессию в список и открываем её,
            // не дожидаясь повторного list — порядок (новые сверху) сохраняется.
            setSessions((prev) => [s, ...prev.filter((p) => p.id !== s.id)]);
            navigate(sessionRoute(s.id));
          }}
        />
      </SessionHeaderProvider>
    </SessionShellContext.Provider>
  );
}

// SidebarAutoClose закрывает выезжающее меню на мобильном при смене маршрута: выбрал
// сессию — меню ушло, второй тап по затемнению не нужен. Реагируем на маршрут, а не на
// каждый обработчик: в меню полдюжины пунктов навигации (сессии, заметки, архив,
// настройки, логотип), и переход из любого должен закрывать панель.
function SidebarAutoClose() {
  const { isMobile, setOpenMobile } = useSidebar();
  const { pathname } = useLocation();
  useEffect(() => {
    if (isMobile) setOpenMobile(false);
  }, [pathname, isMobile, setOpenMobile]);
  return null;
}

// SessionItem — пункт списка сессий: подпись, непрочитанность, действия и подсветка
// активной сессии по совпадению с :sessionId.
// withName возвращает копию сессии с новым именем, если её id совпадает с целевым,
// иначе исходную. Session — protobuf-класс (@bufbuild Message), поэтому клонируем
// через clone(), а не spread, чтобы сохранить прототип и методы.
function withName(s: Session, id: string, name: string): Session {
  if (s.id !== id) return s;
  const copy = s.clone();
  copy.name = name;
  return copy;
}

function SessionItem({
  session,
  grouped = false,
  busy,
  deleting = false,
  archiving = false,
  reloading = false,
  onOpen,
  onDelete,
  onRename,
  onArchive,
  onReloadAgent,
}: {
  session: Session;
  grouped?: boolean;
  busy: boolean;
  deleting?: boolean;
  archiving?: boolean;
  reloading?: boolean;
  onOpen: () => void;
  onDelete: () => void;
  onRename: (name: string) => void;
  onArchive: () => void;
  onReloadAgent: () => void;
}) {
  // locked — сессия в необратимой операции (удаление/архивация): её нельзя открывать,
  // переименовывать, а контент блокирован оверлеем.
  const locked = deleting || archiving;
  const { sessionId } = useParams<{ sessionId: string }>();
  const active = sessionId === session.id;
  // Производная подпись, если пользователь не задал имя.
  const fallback = `${session.agentType} · ${kindLabel(session.kind)}`;
  const fullLabel = session.name || fallback;
  const groupPrefix = `${session.groupLabel} · `;
  const refreshPinned =
    session.kind === SessionKind.ACP && (session.agentOutdated || reloading);
  const label = grouped && fullLabel === session.groupLabel
    ? "Личный чат"
    : grouped && fullLabel.startsWith(groupPrefix)
      ? fullLabel.slice(groupPrefix.length)
      : fullLabel;

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(fullLabel);
  const inputRef = useRef<HTMLInputElement>(null);

  const startEdit = useCallback(() => {
    setDraft(session.name || "");
    setEditing(true);
  }, [session.name]);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
      inputRef.current?.select();
    }
  }, [editing]);

  function commit() {
    setEditing(false);
    const next = draft.trim();
    // Пустой ввод сбрасывает имя на производную подпись (name=""), непустой — задаёт.
    if (next !== (session.name || "")) {
      onRename(next);
    }
  }

  if (editing) {
    return (
      <SidebarMenuItem className={grouped ? "pl-3" : undefined}>
        <input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            } else if (e.key === "Escape") {
              e.preventDefault();
              setEditing(false);
            }
          }}
          placeholder={fallback}
          className="h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-[2px] focus-visible:ring-ring/50"
        />
      </SidebarMenuItem>
    );
  }

  return (
    <SidebarMenuItem className={grouped ? "pl-3" : undefined}>
      <SidebarMenuButton
        isActive={active}
        // Сессию в необратимой операции не открываем: её контент уже блокирован оверлеем.
        onClick={locked ? undefined : onOpen}
        onDoubleClick={(e) => {
          e.stopPropagation();
          if (!locked) startEdit();
        }}
        tooltip={label}
        // Правый паддинг под ряд hover-иконок применяем ТОЛЬКО при наведении/фокусе (как и
        // появление самих иконок): иконки абсолютные (вне потока), поэтому ширину имени задаёт
        // лишь padding — постоянный отступ вечно сжимал бы название. На hover имя ужимается,
        // освобождая место иконкам (ACP их 4, CLI — 2). important (trailing `!`) перебивает
        // базовый `group-has-[menu-action]:pr-8` из sidebarMenuButtonVariants.
        //
        // Подсветку строки вешаем на group-hover/menu-item (весь <li>), а не на :hover самой
        // кнопки: иконки-сиблинги (absolute, поверх) перехватывают hover, между ними щели —
        // из-за этого фон кнопки мигал бы. transition-none: паддинг меняется мгновенно, в такт
        // мгновенному появлению иконок (иначе имя доанимировалось бы уже после их показа).
        className={`rounded-[8px] text-[13px] transition-none! group-hover/menu-item:bg-sidebar-accent group-hover/menu-item:text-sidebar-accent-foreground ${
          refreshPinned
            ? "pr-28! md:pr-8! md:group-hover/menu-item:pr-28! md:group-focus-within/menu-item:pr-28!"
            : session.kind === SessionKind.ACP
            ? "group-hover/menu-item:pr-28! group-focus-within/menu-item:pr-28!"
            : "group-hover/menu-item:pr-16! group-focus-within/menu-item:pr-16!"
        }${locked ? " opacity-60" : ""}`}
      >
        <span className="hidden size-4 shrink-0 items-center justify-center text-[10px] font-medium uppercase group-data-[collapsible=icon]:flex">
          {label[0]}
        </span>
        <span className="truncate group-data-[collapsible=icon]:hidden">{label}</span>
        {session.unread && !active && (
          <span
            title="Есть непрочитанный ответ"
            className="absolute top-1.5 right-2 size-2 rounded-full bg-primary transition-opacity group-hover/menu-item:opacity-0 group-focus-within/menu-item:opacity-0 group-data-[collapsible=icon]:top-1 group-data-[collapsible=icon]:right-1"
          />
        )}
      </SidebarMenuButton>
      {session.kind === SessionKind.ACP && (
        <Tooltip>
          <TooltipTrigger asChild>
            <SidebarMenuAction
              showOnHover={!session.agentOutdated && !reloading}
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                if (!busy) onReloadAgent();
              }}
              aria-label="Перезапустить агента на актуальном окружении"
              className={
                refreshPinned
                  ? `right-1 z-10 text-warning opacity-100 hover:text-warning${reloading ? " disabled:opacity-100" : ""}`
                  : "right-[5.25rem] text-sidebar-foreground/60 hover:text-sidebar-foreground"
              }
            >
              <RefreshCw className={`size-4 ${reloading ? "animate-spin" : ""}`} />
            </SidebarMenuAction>
          </TooltipTrigger>
          <TooltipContent side="right" className="pointer-events-none max-w-64">
            {session.agentOutdated ? (
              <>
                Среда агента запущена на {session.agentVersion}, Brigade обновлён до {import.meta.env.VITE_APP_VERSION ?? "новой версии"}. Перезапустите агента.
              </>
            ) : (
              <>Перезапустить агента на актуальном окружении</>
            )}
          </TooltipContent>
        </Tooltip>
      )}
      {session.kind === SessionKind.ACP && (
        <SidebarMenuAction
          showOnHover
          disabled={busy}
          onClick={(e) => {
            e.stopPropagation();
            if (!busy) onArchive();
          }}
          aria-label="Архивировать сессию"
          // showOnHover прячет кнопку без наведения — на время архивации спиннер виден.
          className={
            refreshPinned
              ? busy
                ? `right-[5.25rem] text-sidebar-foreground/60${archiving ? " opacity-100" : ""}`
                : "right-[5.25rem] text-sidebar-foreground/60 transition-[right,opacity,color] duration-200 md:right-1 md:group-hover/menu-item:right-[5.25rem] md:group-focus-within/menu-item:right-[5.25rem] hover:text-sidebar-foreground"
              : archiving
                ? "right-14 text-sidebar-foreground/60 opacity-100"
                : "right-14 text-sidebar-foreground/60 hover:text-sidebar-foreground"
          }
        >
          {archiving ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <Archive className="size-4" />
          )}
        </SidebarMenuAction>
      )}
      <SidebarMenuAction
        showOnHover
        disabled={busy}
        onClick={(e) => {
          e.stopPropagation();
          if (!busy) startEdit();
        }}
        aria-label="Переименовать сессию"
        className={
          refreshPinned
            ? busy
              ? "right-14 text-sidebar-foreground/60"
              : "right-14 text-sidebar-foreground/60 transition-[right,opacity,color] duration-200 md:right-1 md:group-hover/menu-item:right-14 md:group-focus-within/menu-item:right-14 hover:text-sidebar-foreground"
            : "right-7 text-sidebar-foreground/60 hover:text-sidebar-foreground"
        }
      >
        <Pencil className="size-4" />
      </SidebarMenuAction>
      <SidebarMenuAction
        showOnHover
        disabled={busy}
        onClick={(e) => {
          e.stopPropagation();
          if (!busy) onDelete();
        }}
        aria-label="Удалить сессию"
        // showOnHover прячет кнопку без наведения — на время удаления спиннер
        // остаётся видимым принудительно.
        className={
          refreshPinned
            ? busy
              ? `right-7 text-sidebar-foreground/60${deleting ? " opacity-100" : ""}`
              : "right-7 text-sidebar-foreground/60 transition-[right,opacity,color] duration-200 md:right-1 md:group-hover/menu-item:right-7 md:group-focus-within/menu-item:right-7 hover:text-destructive"
            : deleting
              ? "text-sidebar-foreground/60 opacity-100"
              : "text-sidebar-foreground/60 hover:text-destructive"
        }
      >
        {deleting ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <Trash2 className="size-4" />
        )}
      </SidebarMenuAction>
    </SidebarMenuItem>
  );
}

type GitHubRelease = {
  tag_name: string;
  html_url: string;
};

function VersionInfo() {
  const current = import.meta.env.VITE_APP_VERSION ?? "dev";
  const [release, setRelease] = useState<GitHubRelease | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void fetch("https://api.github.com/repos/grigory51/brigade/releases/latest", {
      headers: { Accept: "application/vnd.github+json" },
      signal: controller.signal,
    })
      .then((response) => response.ok ? response.json() as Promise<GitHubRelease> : null)
      .then((latest) => {
        if (latest && isNewerVersion(current, latest.tag_name)) setRelease(latest);
      })
      .catch(() => {});
    return () => controller.abort();
  }, [current]);

  return (
    <div className="px-2 py-1 text-[11px] text-muted-foreground">
      <div className="flex items-center justify-between gap-2 text-muted-foreground">
        <span>brigade</span>
        <span className="font-medium text-foreground">{current}</span>
      </div>
      {release && (
        <a
          href={release.html_url}
          target="_blank"
          rel="noreferrer"
          className="mt-0.5 flex items-center gap-1 text-primary/80 hover:text-primary"
        >
          <CircleArrowUp className="size-3" />
          Доступна {release.tag_name}
        </a>
      )}
    </div>
  );
}

function isNewerVersion(current: string, latest: string): boolean {
  const parse = (version: string) => /^v?(\d+)\.(\d+)\.(\d+)$/.exec(version)?.slice(1).map(Number);
  const installed = parse(current);
  const available = parse(latest);
  if (!installed || !available) return false;
  for (let i = 0; i < 3; i += 1) {
    if (available[i] !== installed[i]) return available[i] > installed[i];
  }
  return false;
}

// UserMenu — меню пользователя в серверной web-версии. В Brigade.app SidebarFooter не
// рендерится: настройки и версия находятся в стандартном системном меню приложения.
function UserMenu() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const initial = user?.username?.[0]?.toUpperCase() ?? "?";

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              tooltip={user?.username ?? "Пользователь"}
              className="gap-2"
            >
              <Avatar className="size-6 shrink-0">
                <AvatarFallback className="text-xs">{initial}</AvatarFallback>
              </Avatar>
              <span className="truncate">{user?.username ?? "—"}</span>
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-44">
            <DropdownMenuLabel className="truncate">
              {user?.username ?? "—"}
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => navigate("/settings")}>
              <Settings className="size-4" />
              Настройки
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void logout()}>
              <LogOut className="size-4" />
              Выйти
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <VersionInfo />
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

// SessionTopbar — шапка области контента: триггер сворачивания sidebar и слот
// под-хедера активной сессии (title слева, right справа). Содержимое слота
// публикуют экраны сессии через useSessionHeader.
function SessionTopbar() {
  const { title, right } = useSessionHeaderSlot();
  const { state, isMobile } = useSidebar();
  // На десктопе кнопку разворачивания показываем только в свёрнутом состоянии:
  // в развёрнутом её дублирует триггер в шапке sidebar. На мобильном (offcanvas)
  // триггер нужен всегда — он открывает выезжающую панель.
  const showTrigger = isMobile || state === "collapsed";
  const hasContent = Boolean(title || right);

  // Ничего показывать и разворачивать не нужно — шапки нет вовсе, экран отдаёт всю высоту.
  if (!showTrigger && !hasContent) {
    return null;
  }

  // Нужен только триггер (sidebar свёрнут, у экрана нет своей шапки): не занимаем целую
  // полосу-хедер ради одной кнопки — показываем компактный плавающий триггер в углу поверх
  // контента. Позиционируется относительно SidebarInset (он relative).
  if (!hasContent) {
    return (
      <SidebarTrigger className="absolute left-2 top-2 z-20 size-8 rounded-md border bg-background/80 shadow-sm backdrop-blur hover:bg-accent" />
    );
  }

  // Есть заголовок/правый слот — обычная шапка, триггер встроен слева.
  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-3 border-b px-4">
      <div className="flex min-w-0 items-center gap-3">
        {showTrigger && <SidebarTrigger className="-ml-1" />}
        {title && (
          <div className="min-w-0 truncate text-sm text-muted-foreground">
            {title}
          </div>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2">{right}</div>
    </header>
  );
}
