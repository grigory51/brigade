import {
  useCallback,
  useEffect,
  useState,
  type ComponentType,
  type ReactNode,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Bell,
  Bot,
  Check,
  ChevronDown,
  Container,
  Copy,
  Info,
  KeyRound,
  Loader2,
  NotebookText,
  MessageSquareText,
  Plus,
  Plug,
  RefreshCw,
  Server,
  Send,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { agentClient, authClient, mcpClient, notificationClient, telegramClient } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import type { AgentConnection } from "@/api/gen/brigade/v1/agent_pb";
import type { NotificationBackend } from "@/api/gen/brigade/v1/notification_pb";
import type { TelegramBot } from "@/api/gen/brigade/v1/telegram_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { AgentConnectionsSection } from "./AgentConnectionsSection";
import { EnvironmentSection } from "./EnvironmentSection";
import { DesktopEnvironmentsSection } from "./DesktopEnvironmentsSection";
import { McpSection } from "./McpSection";
import { TelegramSection } from "./TelegramSection";
import { ResponseProfilesSection } from "./ResponseProfilesSection";
import {
  Badge,
  Code,
  DangerZone,
  Description,
  ExternalLink,
  FieldLabel,
  Loading,
  SecretNote,
  SectionHeader,
  Toggle,
  errorText,
} from "./ui";

/**
 * SettingsPage — персональные настройки пользователя: колонка разделов слева, детали
 * одного раздела справа. Раздел — часть URL (/settings/:section), поэтому на него можно
 * дать ссылку и работают кнопки «назад/вперёд».
 *
 * Claude лежит не разделом верхнего уровня, а агентом внутри группы «Агенты»: brigade
 * говорит с агентами по ACP, и Claude Code — один из них. Добавление второго ACP-агента
 * не потребует менять навигацию.
 *
 * Секреты (токен Claude, приватный SSH-ключ, токен ntfy) с сервера не приходят никогда —
 * в поле всегда пустой драфт, а состояние показывается флагом «задан».
 */

type SectionId = "agents" | "mcp" | "profiles" | "environments" | "env" | "memory" | "ssh" | "notifications" | "telegram";

const SECTIONS: SectionId[] = ["agents", "mcp", "profiles", "environments", "env", "memory", "ssh", "notifications", "telegram"];

const AGENTS_OPEN_KEY = "brigade.settings.agentsOpen";

export function SettingsPage() {
  const { desktop } = useAuth();
  const { section } = useParams<{ section: string }>();
  const navigate = useNavigate();
  const active =
    section === "ntfy"
      ? "notifications"
      : section === "claude" || section === "codex"
        ? "agents"
      : (SECTIONS as string[]).includes(section ?? "")
        ? (section as SectionId)
        : "agents";

  const [agentsOpen, setAgentsOpen] = useState(
    () => localStorage.getItem(AGENTS_OPEN_KEY) !== "0",
  );
  const [notificationsOpen, setNotificationsOpen] = useState(true);
  const [selectedAgent, setSelectedAgent] = useState("new");
  const [selectedNotification, setSelectedNotification] = useState("");
  const [integrationsOpen, setIntegrationsOpen] = useState(true);
  const [selectedTelegram, setSelectedTelegram] = useState("");
  useEffect(() => {
    localStorage.setItem(AGENTS_OPEN_KEY, agentsOpen ? "1" : "0");
  }, [agentsOpen]);

  // Состояние всех разделов живёт здесь: точки в навигации показывают статус каждого
  // раздела, не открывая его. Статус считается от текущих значений формы — меняется
  // сразу, не дожидаясь сохранения.
  const [agentConnections, setAgentConnections] = useState<AgentConnection[] | null>(null);
  const [remote, setRemote] = useState<string | null>(null);
  const [publicKey, setPublicKey] = useState<string | null>(null);
  const [notifications, setNotifications] = useState<NotificationBackend[] | null>(null);
  const [telegramBots, setTelegramBots] = useState<TelegramBot[] | null>(null);
  const [telegramMode, setTelegramMode] = useState("poll");
  // Счётчик серверов держится здесь ради точки в навигации; сам раздел грузит свои
  // данные и сообщает изменения наверх.
  const [mcpCount, setMcpCount] = useState(0);

  useEffect(() => {
    let alive = true;
    void agentClient.listConnections({})
      .then((result) => alive && setAgentConnections(result.connections))
      .catch(() => alive && setAgentConnections([]));
    void authClient
      .getMemorySettings({})
      .then((r) => alive && setRemote(r.remote))
      .catch(() => alive && setRemote(""));
    void authClient
      .getSSHSettings({})
      .then((r) => alive && setPublicKey(r.publicKey))
      .catch(() => alive && setPublicKey(""));
    void notificationClient
      .listNotificationBackends({})
      .then((r) => alive && setNotifications(r.backends))
      .catch(() => alive && setNotifications([]));
    void mcpClient
      .listServers({})
      .then((r) => alive && setMcpCount(r.servers.length))
      .catch(() => alive && setMcpCount(0));
    void telegramClient
      .listBots({})
      .then((r) => {
        if (!alive) return;
        setTelegramBots(r.bots);
        setTelegramMode(r.mode);
      })
      .catch(() => alive && setTelegramBots([]));
    return () => {
      alive = false;
    };
  }, []);

  const go = useCallback(
    (id: SectionId) => navigate(`/settings/${id}`),
    [navigate],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* px-5 — заголовок стоит на одной вертикали с иконками пунктов ниже
          (отступ колонки 10px + внутренний отступ строки 10px). */}
      <header className="shrink-0 border-b px-5 pt-[22px] pb-4">
        <h1 className="text-[20px] font-semibold tracking-[-0.01em]">
          Настройки
        </h1>
      </header>

      <div className="flex min-h-0 flex-1">
        <nav className="flex w-[222px] shrink-0 flex-col gap-0.5 overflow-y-auto border-r px-2.5 pt-4 pb-5">
          <NavRow
            icon={Bot}
            label="Агенты"
            onClick={() => setAgentsOpen((v) => !v)}
            trailing={
              <ChevronDown
                className={cn(
                  "size-3.5 text-muted-foreground/70 transition-transform duration-[180ms]",
                  !agentsOpen && "-rotate-90",
                )}
              />
            }
          />
          {agentsOpen && (
            <div className="ml-[19px] border-l pl-[11px]">
              {agentConnections?.map((connection) => (
                <button
                  key={connection.id}
                  type="button"
                  onClick={() => { setSelectedAgent(connection.id); go("agents"); }}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] transition-colors",
                    active === "agents" && selectedAgent === connection.id ? "bg-card text-foreground" : "text-[#e7e5df] hover:bg-card hover:text-foreground",
                  )}
                >
                  <AgentMark agent={connection.agentType === "codex" ? "codex" : "claude"} />
                  <span className="min-w-0 flex-1 truncate text-left">{connection.name}</span>
                </button>
              ))}
              <button
                type="button"
                onClick={() => { setSelectedAgent("new"); go("agents"); }}
                className={cn(
                  "flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] transition-colors",
                  active === "agents" && selectedAgent === "new"
                    ? "bg-card text-foreground"
                    : "text-muted-foreground hover:bg-card hover:text-foreground",
                )}
              >
                <Plus className="size-3.5" />
                Добавить
              </button>
            </div>
          )}
          <NavRow
            icon={Plug}
            label="MCP-серверы"
            active={active === "mcp"}
            onClick={() => go("mcp")}
            trailing={<StatusDot on={mcpCount > 0} />}
          />
          <NavRow
            icon={MessageSquareText}
            label="Профили ответов"
            active={active === "profiles"}
            onClick={() => go("profiles")}
          />
          {desktop && (
            <NavRow
              icon={Server}
              label="Окружения"
              active={active === "environments"}
              onClick={() => go("environments")}
            />
          )}
          <NavRow
            icon={Container}
            label="Среда агента"
            active={active === "env"}
            onClick={() => go("env")}
          />
          <NavRow
            icon={NotebookText}
            label="Память"
            active={active === "memory"}
            onClick={() => go("memory")}
            trailing={<StatusDot on={Boolean(remote?.trim())} />}
          />
          <NavRow
            icon={KeyRound}
            label="SSH-ключ"
            active={active === "ssh"}
            onClick={() => go("ssh")}
            trailing={<StatusDot on={Boolean(publicKey)} />}
          />
          <NavRow
            icon={Send}
            label="Интеграции"
            onClick={() => setIntegrationsOpen((value) => !value)}
            trailing={
              <ChevronDown
                className={cn(
                  "size-3.5 text-muted-foreground/70 transition-transform duration-[180ms]",
                  !integrationsOpen && "-rotate-90",
                )}
              />
            }
          />
          {integrationsOpen && (
            <div className="ml-[19px] border-l pl-[11px]">
              {telegramBots?.map((bot) => (
                <button
                  key={bot.id}
                  type="button"
                  onClick={() => {
                    setSelectedTelegram(bot.id);
                    go("telegram");
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] transition-colors",
                    active === "telegram" && selectedTelegram === bot.id
                      ? "bg-card text-foreground"
                      : "text-[#e7e5df] hover:bg-card hover:text-foreground",
                  )}
                >
                  <img src="https://cdn.simpleicons.org/telegram" alt="" className="size-3.5" />
                  <span className="min-w-0 flex-1 truncate text-left">@{bot.username}</span>
                  <StatusDot on={bot.ownerConnected} />
                </button>
              ))}
              <button
                type="button"
                onClick={() => {
                  setSelectedTelegram("new");
                  go("telegram");
                }}
                className="flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] text-muted-foreground transition-colors hover:bg-card hover:text-foreground"
              >
                <Plus className="size-3.5" />
                Telegram Bot
              </button>
            </div>
          )}
          <NavRow
            icon={Bell}
            label="Уведомления"
            onClick={() => setNotificationsOpen((v) => !v)}
            trailing={
              <ChevronDown
                className={cn(
                  "size-3.5 text-muted-foreground/70 transition-transform duration-[180ms]",
                  !notificationsOpen && "-rotate-90",
                )}
              />
            }
          />
          {notificationsOpen && (
            <div className="ml-[19px] border-l pl-[11px]">
              {notifications?.map((backend) => (
                <button
                  key={backend.id}
                  type="button"
                  onClick={() => {
                    setSelectedNotification(backend.id);
                    go("notifications");
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] transition-colors",
                    active === "notifications" && selectedNotification === backend.id
                      ? "bg-card text-foreground"
                      : "text-[#e7e5df] hover:bg-card hover:text-foreground",
                  )}
                >
                  <span className="min-w-0 flex-1 truncate text-left">{backend.name}</span>
                  <span className="font-mono text-[10px] text-muted-foreground">{backend.kind}</span>
                </button>
              ))}
              <button
                type="button"
                onClick={() => {
                  setSelectedNotification("new");
                  go("notifications");
                }}
                className="flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] text-muted-foreground transition-colors hover:bg-card hover:text-foreground"
              >
                <Plus className="size-3.5" />
                Добавить
              </button>
            </div>
          )}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto scroll-smooth">
          {/* key — чтобы смена раздела заново проигрывала вход и сбрасывала локальное
              состояние деталей (подтверждение перевыпуска ключа, «Скопировано»). */}
          <div
            key={active}
            className="mx-auto flex max-w-[680px] animate-[section-in_0.24s_cubic-bezier(0.2,0.8,0.2,1)] flex-col gap-[18px] px-[34px] pt-6 pb-[90px]"
          >
            {active === "agents" && (
              <AgentConnectionsSection
                connections={agentConnections}
                selectedId={selectedAgent}
                onSelect={setSelectedAgent}
                onChange={setAgentConnections}
              />
            )}
            {active === "mcp" && <McpSection onCountChange={setMcpCount} />}
            {active === "profiles" && <ResponseProfilesSection />}
            {active === "environments" && <DesktopEnvironmentsSection />}
            {active === "env" && <EnvironmentSection />}
            {active === "memory" && (
              <MemorySection
                remote={remote}
                onChange={setRemote}
              />
            )}
            {active === "ssh" && (
              <SshSection publicKey={publicKey} onChange={setPublicKey} />
            )}
            {active === "notifications" && (
              <NotificationsSection
                backends={notifications}
                selectedId={selectedNotification}
                onSelect={setSelectedNotification}
                onChange={setNotifications}
              />
            )}
            {active === "telegram" && (
              <TelegramSection
                bots={telegramBots}
                mode={telegramMode}
                selectedId={selectedTelegram}
                onSelect={setSelectedTelegram}
                onChange={setTelegramBots}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

// ─── Общие элементы разделов ──────────────────────────────────────────────────

function NavRow({
  icon: Icon,
  label,
  active = false,
  onClick,
  trailing,
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  active?: boolean;
  onClick: () => void;
  trailing?: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex items-center gap-2 rounded-lg px-2.5 py-2 text-[13px] transition-colors",
        active
          ? "bg-card text-foreground"
          : "text-[#e7e5df] hover:bg-card hover:text-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" />
      <span className="flex-1 text-left">{label}</span>
      {trailing}
    </button>
  );
}

function StatusDot({ on }: { on: boolean }) {
  return (
    <span
      className={cn("size-1.5 shrink-0 rounded-full", on ? "bg-success" : "bg-input")}
    />
  );
}

// AgentMark — визуально разные марки агентов в навигации и заголовке раздела.
function AgentMark({
  agent,
  large = false,
}: {
  agent: "claude" | "codex";
  large?: boolean;
}) {
  const size = large ? "size-[30px]" : "size-3.5";
  return (
    <span className={cn("flex shrink-0 items-center justify-center", large ? "size-[34px]" : "size-4")}>
      {agent === "claude" ? (
        <img src="https://cdn.simpleicons.org/claude" alt="" className={size} />
      ) : (
        <svg viewBox="0 0 20 20" aria-hidden="true" className={cn(size, "fill-foreground")}>
          <path d="M11.248 18.25q-.825 0-1.568-.314a4.3 4.3 0 0 1-1.32-.874 4 4 0 0 1-1.304.214 4 4 0 0 1-2.046-.544 4.27 4.27 0 0 1-1.518-1.485 4 4 0 0 1-.56-2.095q0-.48.131-1.04A4.4 4.4 0 0 1 2.04 10.71a4.07 4.07 0 0 1 .017-3.4 4.2 4.2 0 0 1 1.056-1.418 3.8 3.8 0 0 1 1.6-.842 3.9 3.9 0 0 1 .76-1.683q.593-.759 1.451-1.188a4.04 4.04 0 0 1 1.832-.429q.825 0 1.567.313.742.314 1.32.875a4 4 0 0 1 1.304-.215q1.106 0 2.046.545a4.14 4.14 0 0 1 1.501 1.485q.578.941.578 2.095 0 .48-.132 1.04.66.61 1.023 1.419.363.792.363 1.666 0 .892-.38 1.717a4.3 4.3 0 0 1-1.072 1.435 3.8 3.8 0 0 1-1.584.825 3.8 3.8 0 0 1-.775 1.683 4.06 4.06 0 0 1-1.436 1.188 4.04 4.04 0 0 1-1.832.429m-4.076-2.062q.825 0 1.435-.347l3.103-1.782a.36.36 0 0 0 .164-.313v-1.42L7.881 14.62a.67.67 0 0 1-.726 0l-3.118-1.798a.5.5 0 0 1-.017.115v.198q0 .841.396 1.551.413.693 1.139 1.089a3.2 3.2 0 0 0 1.617.412m.165-2.69a.4.4 0 0 0 .181.05q.083 0 .165-.05l1.238-.71-3.977-2.31a.7.7 0 0 1-.363-.643v-3.58q-.825.362-1.32 1.122a2.9 2.9 0 0 0-.495 1.65q0 .809.413 1.55.412.743 1.072 1.123zm3.91 3.663q.875 0 1.585-.396a2.96 2.96 0 0 0 1.534-2.64v-3.564a.32.32 0 0 0-.165-.297l-1.254-.726v4.604a.7.7 0 0 1-.363.643l-3.119 1.799a3 3 0 0 0 1.783.577m.627-6.039V8.878L10.01 7.822 8.129 8.878v2.244l1.881 1.056zM7.057 5.859a.7.7 0 0 1 .363-.644l3.119-1.798a3 3 0 0 0-1.782-.578q-.874 0-1.584.396A2.96 2.96 0 0 0 6.05 4.324a3.07 3.07 0 0 0-.396 1.551v3.547q0 .199.165.314l1.237.726zm8.383 7.887q.825-.364 1.303-1.123.495-.758.495-1.65a3.15 3.15 0 0 0-.412-1.55q-.413-.743-1.073-1.123l-3.086-1.782q-.099-.065-.181-.049a.3.3 0 0 0-.165.05l-1.238.692 3.993 2.327a.6.6 0 0 1 .264.264.64.64 0 0 1 .1.363zm-3.317-8.382a.63.63 0 0 1 .726 0l3.135 1.831v-.297q0-.792-.396-1.501a2.86 2.86 0 0 0-1.105-1.155q-.71-.43-1.65-.43-.825 0-1.436.347L8.294 5.941a.36.36 0 0 0-.165.314v1.418z" />
        </svg>
      )}
    </span>
  );
}

// ─── Память ───────────────────────────────────────────────────────────────────

function MemorySection({
  remote,
  onChange,
}: {
  remote: string | null;
  onChange: (v: string) => void;
}) {
  const [saving, setSaving] = useState(false);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const res = await authClient.setMemorySettings({
        remote: (remote ?? "").trim(),
      });
      onChange(res.remote);
      toast.success("Настройки памяти сохранены");
    } catch (err) {
      toast.error(errorText(err, "Не удалось сохранить настройки памяти"));
    } finally {
      setSaving(false);
    }
  }, [remote, onChange]);

  if (remote === null) return <Loading />;
  const filled = Boolean(remote.trim());

  return (
    <>
      <SectionHeader
        title="Память"
        badge={<Badge on={filled}>{filled ? "подключена" : "выключена"}</Badge>}
      >
        <Description>
          Приватный git-репозиторий заметок: агент читает его в начале сессии и
          дописывает в конце. Репозиторий один на пользователя и общий для всех агентов. Для
          git@-remote используется{" "}
          <Link className="text-foreground underline underline-offset-2" to="/settings/ssh">
            SSH-ключ агента
          </Link>{" "}
          — отдельный ключ не нужен.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        <FieldLabel>Git-remote</FieldLabel>
        <div className="flex items-start gap-2">
          <Input
            placeholder="git@gitlab.com:you/brigade-memory.git"
            autoComplete="off"
            value={remote}
            onChange={(e) => onChange(e.target.value)}
            className="h-[41px] flex-1 bg-[#1c1b1a] font-mono text-[12.5px] focus-visible:border-[#5a4034]"
          />
          <Button className="h-[41px]" disabled={saving} onClick={() => void save()}>
            {saving && <Loader2 className="size-4 animate-spin" />}
            Сохранить
          </Button>
        </div>
        <p
          className={cn(
            "flex items-start gap-1.5 text-[11.5px] leading-[1.55]",
            filled ? "text-[#6c695f]" : "text-warning",
          )}
        >
          <Info className="mt-0.5 size-3 shrink-0" />
          <span>
            {filled
              ? "Агент клонирует репозиторий в контейнер сессии при старте"
              : "Память выключена — агент начинает каждую сессию с нуля"}
          </span>
        </p>
      </div>
    </>
  );
}

// ─── SSH-ключ агента ──────────────────────────────────────────────────────────

function SshSection({
  publicKey,
  onChange,
}: {
  publicKey: string | null;
  onChange: (v: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const [regen, setRegen] = useState<"idle" | "ask">("idle");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1600);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = useCallback(() => {
    if (!publicKey) return;
    void navigator.clipboard
      .writeText(publicKey)
      .then(() => setCopied(true))
      .catch(() => toast.error("Не удалось скопировать"));
  }, [publicKey]);

  const regenerate = useCallback(async () => {
    setBusy(true);
    try {
      const res = await authClient.regenerateSSHKey({});
      onChange(res.publicKey);
      setRegen("idle");
      setCopied(false); // ключ другой — прежнее «Скопировано» вводило бы в заблуждение
      toast.success("SSH-ключ перевыпущен — обновите ключ в GitHub");
    } catch (err) {
      toast.error(errorText(err, "Не удалось перевыпустить ключ"));
    } finally {
      setBusy(false);
    }
  }, [onChange]);

  if (publicKey === null) return <Loading />;

  return (
    <>
      <SectionHeader
        title="SSH-ключ агента"
        badge={<Badge on={Boolean(publicKey)}>ключ создан</Badge>}
      >
        <Description>
          Стабильный ключ, который brigade подкладывает в контейнер ваших сессий.
          Добавьте публичный ключ в{" "}
          <ExternalLink href="https://github.com/settings/keys">
            GitHub → SSH keys
          </ExternalLink>{" "}
          (или как deploy key репозитория) — и агент сможет пушить по{" "}
          <Code>git@github.com</Code>.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <FieldLabel>Публичный ключ</FieldLabel>
          <Button
            variant="outline"
            size="sm"
            onClick={copy}
            className={cn(
              "h-7 gap-1.5 text-xs",
              copied && "border-success/35 text-[#8dbf82]",
            )}
          >
            {copied ? <Check className="size-3" /> : <Copy className="size-3" />}
            {copied ? "Скопировано" : "Скопировать"}
          </Button>
        </div>
        <div className="rounded-[10px] border bg-[#1c1b1a] px-[13px] py-3 font-mono text-[11.5px] leading-[1.7] break-all text-muted-foreground select-all">
          {publicKey}
        </div>
        <SecretNote>
          Приватная часть хранится на сервере зашифрованной и наружу не отдаётся
        </SecretNote>
      </div>

      <DangerZone
        title="Перевыпустить ключ"
        hint="Старый публичный ключ в GitHub перестанет работать"
      >
        {/* Подтверждение инлайновое: window.confirm выбивает из интерфейса и на нём
            невозможно объяснить последствия в том же визуальном языке. */}
        {regen === "idle" ? (
          <Button
            variant="outline"
            className="gap-1.5"
            onClick={() => setRegen("ask")}
          >
            <RefreshCw className="size-3.5" />
            Перевыпустить
          </Button>
        ) : (
          <div className="flex shrink-0 items-center gap-2">
            <Button
              disabled={busy}
              onClick={() => void regenerate()}
              className="bg-destructive text-white hover:bg-destructive/90"
            >
              {busy && <Loader2 className="size-4 animate-spin" />}
              Да, новый ключ
            </Button>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => setRegen("idle")}
            >
              Отмена
            </Button>
          </div>
        )}
      </DangerZone>
    </>
  );
}

// ─── Уведомления ──────────────────────────────────────────────────────────────

/** Событие уведомления: ключ совпадает с backend (internal/notify). */
const NTFY_EVENTS: { key: string; label: string; hint: string }[] = [
  {
    key: "turn_end",
    label: "Агент завершил ответ",
    hint: "Turn закончился, агент ждёт вас",
  },
  {
    key: "error",
    label: "Ошибка в turn'е",
    hint: "Агент упал или инструмент вернул ошибку",
  },
];

type NotificationDraft = {
  id: string;
  kind: string;
  name: string;
  server: string;
  topic: string;
  tokenSet: boolean;
  events: string[];
};

const emptyNotification = (): NotificationDraft => ({
  id: "",
  kind: "ntfy",
  name: "ntfy",
  server: "",
  topic: "",
  tokenSet: false,
  events: ["turn_end", "error"],
});

function NotificationsSection({
  backends,
  selectedId,
  onSelect,
  onChange,
}: {
  backends: NotificationBackend[] | null;
  selectedId: string;
  onSelect: (id: string) => void;
  onChange: (backends: NotificationBackend[]) => void;
}) {
  const [draft, setDraft] = useState<NotificationDraft>(emptyNotification);
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    if (!backends) return;
    const selected =
      selectedId === "new"
        ? undefined
        : backends.find((backend) => backend.id === selectedId) ?? backends[0];
    if (!selected) {
      setDraft(emptyNotification());
      return;
    }
    if (!selectedId) onSelect(selected.id);
    setDraft({
      id: selected.id,
      kind: selected.kind,
      name: selected.name,
      server: selected.ntfy?.server ?? "",
      topic: selected.ntfy?.topic ?? "",
      tokenSet: selected.ntfy?.tokenSet ?? false,
      events: selected.events,
    });
    setToken("");
  }, [backends, selectedId, onSelect]);

  if (!backends) return <Loading />;

  const patch = (next: Partial<NotificationDraft>) =>
    setDraft((current) => ({ ...current, ...next }));
  const toggle = (event: string) =>
    patch({
      events: draft.events.includes(event)
        ? draft.events.filter((item) => item !== event)
        : [...draft.events, event],
    });

  const save = async () => {
    setSaving(true);
    try {
      const saved = await notificationClient.saveNotificationBackend({
        backend: {
          id: draft.id,
          kind: draft.kind,
          name: draft.name.trim(),
          events: draft.events,
          ntfy: { server: draft.server.trim(), topic: draft.topic.trim() },
        },
        secret: token,
      });
      onChange([...backends.filter((item) => item.id !== saved.id), saved]);
      onSelect(saved.id);
      setToken("");
      toast.success("Подключение сохранено");
    } catch (err) {
      toast.error(errorText(err, "Не удалось сохранить подключение"));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!draft.id) return;
    try {
      await notificationClient.deleteNotificationBackend({ id: draft.id });
      const next = backends.filter((item) => item.id !== draft.id);
      onChange(next);
      onSelect(next[0]?.id ?? "new");
      toast.success("Подключение удалено");
    } catch (err) {
      toast.error(errorText(err, "Не удалось удалить подключение"));
    }
  };

  const test = async () => {
    setTesting(true);
    try {
      await notificationClient.testNotificationBackend({ id: draft.id });
      toast.success("Тестовое уведомление отправлено");
    } catch (err) {
      toast.error(errorText(err, "Не удалось отправить уведомление"));
    } finally {
      setTesting(false);
    }
  };

  return (
    <>
      <SectionHeader
        title={draft.id ? draft.name : "Новое подключение"}
      >
        <Description>
          Подключите один или несколько способов доставки. Каждое подключение получает
          выбранные события независимо от остальных.
        </Description>
      </SectionHeader>

      {!draft.id && (
        <div className="flex flex-col gap-2">
          <FieldLabel>Backend</FieldLabel>
          <Select value={draft.kind} onValueChange={(kind) => patch({ kind })}>
            <SelectTrigger className="h-[41px] w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="ntfy">ntfy</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )}

      <div className="flex flex-col gap-2">
        <FieldLabel>Название</FieldLabel>
        <Input
          value={draft.name}
          onChange={(event) => patch({ name: event.target.value })}
          placeholder="Рабочий телефон"
          className="h-[41px] bg-[#1c1b1a]"
        />
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel>Топик</FieldLabel>
        <Input
          value={draft.topic}
          onChange={(event) => patch({ topic: event.target.value })}
          placeholder="brigade-alerts-a8f3"
          autoComplete="off"
          className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px]"
        />
        <p className="text-[11.5px] leading-[1.55] text-[#6c695f]">
          Кто знает топик — читает ваши уведомления. Придумайте неочевидный.
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3.5">
        <div className="flex flex-col gap-2">
          <FieldLabel>Сервер</FieldLabel>
          <Input
            value={draft.server}
            onChange={(event) => patch({ server: event.target.value })}
            placeholder="https://ntfy.sh"
            autoComplete="off"
            className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px]"
          />
          <p className="text-[11.5px] leading-[1.55] text-[#6c695f]">
            Пусто — публичный ntfy.sh
          </p>
        </div>
        <div className="flex flex-col gap-2">
          <FieldLabel>
            {draft.tokenSet ? "Новый токен доступа" : "Токен доступа · необязательно"}
          </FieldLabel>
          <Input
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            placeholder={draft.tokenSet ? "Пусто — не менять" : "tk_…"}
            autoComplete="off"
            className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px]"
          />
          <SecretNote>Шифруется на сервере, обратно не отдаётся</SecretNote>
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel>События</FieldLabel>
        <div className="divide-y overflow-hidden rounded-[11px] border bg-[#1c1b1a]">
          {NTFY_EVENTS.map((event) => (
            <button
              key={event.key}
              type="button"
              onClick={() => toggle(event.key)}
              className="flex w-full items-center gap-3 px-3.5 py-3 text-left transition-colors hover:bg-[#232221]"
            >
              <span className="min-w-0 flex-1">
                <span className="block text-[13px]">{event.label}</span>
                <span className="block text-[11.5px] text-[#6c695f]">{event.hint}</span>
              </span>
              <Toggle on={draft.events.includes(event.key)} />
            </button>
          ))}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <Button
          disabled={saving || !draft.name.trim() || !draft.topic.trim()}
          onClick={() => void save()}
        >
          {saving && <Loader2 className="size-4 animate-spin" />}
          Сохранить
        </Button>
        {draft.id && (
          <>
            <Button variant="outline" disabled={testing} onClick={() => void test()}>
              {testing ? <Loader2 className="size-4 animate-spin" /> : <Send className="size-4" />}
              Тест
            </Button>
            <Button variant="ghost" className="ml-auto text-destructive" onClick={() => void remove()}>
              <Trash2 className="size-4" />
              Удалить
            </Button>
          </>
        )}
      </div>
    </>
  );
}
