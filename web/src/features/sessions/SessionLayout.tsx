import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
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
  GitBranch,
  Loader2,
  LogOut,
  MessagesSquare,
  NotebookPen,
  Pencil,
  Plus,
  RefreshCw,
  Settings,
  Terminal,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { sessionClient } from "@/api/client";
import {
  Session,
  SessionKind,
  SessionStatus,
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
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
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
  const [busyId, setBusyId] = useState<string | null>(null);
  // deletingIds — сессии, удаление которых сейчас выполняется на сервере. Отдельно от
  // busyId (fork): teardown контейнера/процесса занимает до ~15 секунд, и без индикации
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
      setState("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

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

  const onFork = useCallback(
    async (id: string) => {
      setBusyId(id);
      try {
        const res = await sessionClient.fork({ sessionId: id });
        const branch = res.session;
        if (branch) {
          // Ветка добавляется в список и открывается сразу — как при создании сессии.
          setSessions((prev) => [branch, ...prev.filter((p) => p.id !== branch.id)]);
          navigate(sessionRoute(branch.id));
        }
      } catch (err) {
        toast.error(
          err instanceof ConnectError
            ? err.rawMessage
            : "Не удалось создать ветку",
        );
      } finally {
        setBusyId(null);
      }
    },
    [navigate],
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

  const openCreate = useCallback(() => setCreateOpen(true), []);

  // Дерево веток в сайдбаре: корневые сессии в исходном порядке (новые сверху), после
  // каждой — её ветки с отступом. Ветка с удалённым родителем показывается как корневая.
  const ordered = useMemo(() => {
    const byParent = new Map<string, Session[]>();
    const ids = new Set(sessions.map((s) => s.id));
    const roots: Session[] = [];
    for (const s of sessions) {
      if (s.parentId && ids.has(s.parentId)) {
        const list = byParent.get(s.parentId) ?? [];
        list.push(s);
        byParent.set(s.parentId, list);
      } else {
        roots.push(s);
      }
    }
    const out: { session: Session; depth: number }[] = [];
    const walk = (s: Session, depth: number) => {
      out.push({ session: s, depth });
      for (const child of byParent.get(s.id) ?? []) {
        walk(child, depth + 1);
      }
    };
    roots.forEach((s) => walk(s, 0));
    return out;
  }, [sessions]);

  return (
    <SessionShellContext.Provider value={{ openCreate }}>
      <SessionHeaderProvider>
        <SidebarProvider>
          <Sidebar collapsible="icon">
            <SidebarHeader>
              <div className="flex items-center justify-between gap-2 group-data-[collapsible=icon]:justify-center">
                <Link
                  to="/sessions"
                  className="flex shrink-0 items-center gap-2 px-1 font-semibold"
                >
                  <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-sidebar-primary text-sm font-bold text-sidebar-primary-foreground">
                    b
                  </span>
                  <span className="group-data-[collapsible=icon]:hidden">
                    brigade
                  </span>
                </Link>
                <SidebarTrigger className="group-data-[collapsible=icon]:hidden" />
              </div>
              <Button
                size="sm"
                onClick={openCreate}
                className="justify-start gap-2 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0"
              >
                <Plus className="size-4 shrink-0" />
                <span className="group-data-[collapsible=icon]:hidden">
                  Новая сессия
                </span>
              </Button>
            </SidebarHeader>

            <SidebarContent>
              <SidebarGroup>
                <SidebarGroupLabel className="justify-between pr-1">
                  Сессии
                  <button
                    type="button"
                    onClick={() => void load()}
                    disabled={state === "loading"}
                    aria-label="Обновить список"
                    className="flex size-5 items-center justify-center rounded text-sidebar-foreground/60 transition-colors hover:text-sidebar-foreground disabled:opacity-50"
                  >
                    <RefreshCw
                      className={
                        state === "loading"
                          ? "size-3.5 animate-spin"
                          : "size-3.5"
                      }
                    />
                  </button>
                </SidebarGroupLabel>
                <SidebarGroupContent>
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

                    {state === "ready" &&
                      ordered.map(({ session: s, depth }) => (
                        <SessionItem
                          key={s.id}
                          session={s}
                          depth={depth}
                          busy={
                            busyId === s.id ||
                            deletingIds.has(s.id) ||
                            archivingIds.has(s.id)
                          }
                          deleting={deletingIds.has(s.id)}
                          archiving={archivingIds.has(s.id)}
                          onOpen={() => navigate(sessionRoute(s.id))}
                          onDelete={() => void onDelete(s.id)}
                          onRename={(name) => void onRename(s.id, name)}
                          onFork={() => void onFork(s.id)}
                          onArchive={() => void onArchive(s.id)}
                        />
                      ))}
                  </SidebarMenu>
                </SidebarGroupContent>
              </SidebarGroup>
            </SidebarContent>

            <SidebarFooter>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    onClick={() => navigate("/memory")}
                    isActive={location.pathname.startsWith("/memory")}
                    tooltip="Заметки"
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
                  >
                    <Archive className="size-4" />
                    Архив
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
              <UserMenu />
            </SidebarFooter>
            <SidebarRail />
          </Sidebar>

          <SidebarInset className="h-svh min-h-0">
            <SessionTopbar />
            <div className="relative flex min-h-0 flex-1 flex-col">
              <div className="min-h-0 flex-1">
                <Outlet />
              </div>
              {/* Вспомогательный шелл активной сессии. key пересоздаёт панель при
                  переключении сессии: шелл принадлежит конкретной сессии. */}
              {activeId && <ShellPanel key={activeId} sessionId={activeId} />}
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

// SessionItem — пункт списка сессий: иконка типа, подпись (агент + тип), точка
// статуса, действие удаления и подсветка активной сессии по совпадению с :sessionId.
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
  depth = 0,
  busy,
  deleting = false,
  archiving = false,
  onOpen,
  onDelete,
  onRename,
  onFork,
  onArchive,
}: {
  session: Session;
  depth?: number;
  busy: boolean;
  deleting?: boolean;
  archiving?: boolean;
  onOpen: () => void;
  onDelete: () => void;
  onRename: (name: string) => void;
  onFork: () => void;
  onArchive: () => void;
}) {
  // locked — сессия в необратимой операции (удаление/архивация): её нельзя открывать,
  // переименовывать, а контент блокирован оверлеем.
  const locked = deleting || archiving;
  const { sessionId } = useParams<{ sessionId: string }>();
  const active = sessionId === session.id;
  const KindIcon = session.kind === SessionKind.ACP ? MessagesSquare : Terminal;
  // Производная подпись, если пользователь не задал имя.
  const fallback = `${session.agentType} · ${kindLabel(session.kind)}`;
  const label = session.name || fallback;

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(label);
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
      <SidebarMenuItem>
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
    <SidebarMenuItem>
      <SidebarMenuButton
        isActive={active}
        // Сессию в необратимой операции не открываем: её контент уже блокирован оверлеем.
        onClick={locked ? undefined : onOpen}
        onDoubleClick={(e) => {
          e.stopPropagation();
          if (!locked) startEdit();
        }}
        tooltip={label}
        // Правый паддинг под ряд hover-иконок: у ACP их 4 (архив/ветка/переименовать/
        // удалить), у CLI — 2 (переименовать/удалить), поэтому места нужно больше.
        className={`${
          session.kind === SessionKind.ACP ? "pr-28" : "pr-16"
        }${locked ? " opacity-60" : ""}`}
        // Ветки визуально вкладываются под родителя (см. ordered в SessionLayout).
        style={depth > 0 ? { paddingLeft: `${8 + depth * 16}px` } : undefined}
      >
        <span className="relative shrink-0">
          {depth > 0 ? (
            <GitBranch className="size-4" />
          ) : (
            <KindIcon className="size-4" />
          )}
          <StatusDot status={session.status} />
        </span>
        <span className="truncate">{label}</span>
      </SidebarMenuButton>
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
            archiving
              ? "right-[5.25rem] text-sidebar-foreground/60 opacity-100"
              : "right-[5.25rem] text-sidebar-foreground/60 hover:text-sidebar-foreground"
          }
        >
          {archiving ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <Archive className="size-4" />
          )}
        </SidebarMenuAction>
      )}
      {session.kind === SessionKind.ACP && (
        <SidebarMenuAction
          showOnHover
          disabled={busy}
          onClick={(e) => {
            e.stopPropagation();
            onFork();
          }}
          aria-label="Создать ветку"
          className="right-14 text-sidebar-foreground/60 hover:text-sidebar-foreground"
        >
          <GitBranch className="size-4" />
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
        className="right-7 text-sidebar-foreground/60 hover:text-sidebar-foreground"
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
          deleting
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

// StatusDot — маленький индикатор состояния у иконки сессии: зелёный для running,
// красный для failed; для остальных состояний не показывается.
function StatusDot({ status }: { status: SessionStatus }) {
  if (status === SessionStatus.RUNNING) {
    return (
      <span className="absolute -right-0.5 -bottom-0.5 size-2 rounded-full bg-success ring-2 ring-sidebar" />
    );
  }
  if (status === SessionStatus.FAILED) {
    return (
      <span className="absolute -right-0.5 -bottom-0.5 size-2 rounded-full bg-destructive ring-2 ring-sidebar" />
    );
  }
  return null;
}

// UserMenu — пункт пользователя в подвале sidebar: аватар, имя и выход. Логика
// перенесена из прежнего Layout без изменений (useAuth → logout).
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

  // Шапку не рендерим вовсе, когда показывать в ней нечего (нет заголовка, нет
  // правого слота и триггер не нужен) — экран отдаёт всю высоту содержимому.
  if (!showTrigger && !title && !right) {
    return null;
  }

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
