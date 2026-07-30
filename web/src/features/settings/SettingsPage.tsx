import {
  useCallback,
  useEffect,
  useState,
  type ComponentType,
  type ReactNode,
} from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  Bell,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  Container,
  Copy,
  Eye,
  EyeOff,
  Info,
  KeyRound,
  Loader2,
  NotebookText,
  Plug,
  RefreshCw,
  Send,
} from "lucide-react";
import { toast } from "sonner";
import { authClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { EnvironmentSection } from "./EnvironmentSection";
import { McpSection } from "./McpSection";
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

type SectionId = "claude" | "mcp" | "env" | "memory" | "ssh" | "ntfy";

const SECTIONS: SectionId[] = ["claude", "mcp", "env", "memory", "ssh", "ntfy"];

const AGENTS_OPEN_KEY = "brigade.settings.agentsOpen";

export function SettingsPage() {
  const { section } = useParams<{ section: string }>();
  const navigate = useNavigate();
  const active = (SECTIONS as string[]).includes(section ?? "")
    ? (section as SectionId)
    : "claude";

  const [agentsOpen, setAgentsOpen] = useState(
    () => localStorage.getItem(AGENTS_OPEN_KEY) !== "0",
  );
  useEffect(() => {
    localStorage.setItem(AGENTS_OPEN_KEY, agentsOpen ? "1" : "0");
  }, [agentsOpen]);

  // Состояние всех разделов живёт здесь: точки в навигации показывают статус каждого
  // раздела, не открывая его. Статус считается от текущих значений формы — меняется
  // сразу, не дожидаясь сохранения.
  const [claudeTokenSet, setClaudeTokenSet] = useState<boolean | null>(null);
  const [remote, setRemote] = useState<string | null>(null);
  const [publicKey, setPublicKey] = useState<string | null>(null);
  const [ntfy, setNtfy] = useState<NtfyState | null>(null);
  // Счётчик серверов держится здесь ради точки в навигации; сам раздел грузит свои
  // данные и сообщает изменения наверх.
  const [mcpCount, setMcpCount] = useState(0);
  const [imageCount, setImageCount] = useState(0);

  useEffect(() => {
    let alive = true;
    void authClient
      .getClaudeSettings({})
      .then((r) => alive && setClaudeTokenSet(r.tokenSet))
      .catch(() => alive && setClaudeTokenSet(false));
    void authClient
      .getMemorySettings({})
      .then((r) => alive && setRemote(r.remote))
      .catch(() => alive && setRemote(""));
    void authClient
      .getSSHSettings({})
      .then((r) => alive && setPublicKey(r.publicKey))
      .catch(() => alive && setPublicKey(""));
    void authClient
      .getNtfySettings({})
      .then(
        (r) =>
          alive &&
          setNtfy({
            server: r.server,
            topic: r.topic,
            tokenSet: r.tokenSet,
            events: r.events,
          }),
      )
      .catch(
        () =>
          alive &&
          setNtfy({ server: "", topic: "", tokenSet: false, events: [] }),
      );
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
              <button
                type="button"
                onClick={() => go("claude")}
                className={cn(
                  "flex w-full items-center gap-2 rounded-[7px] px-[9px] py-[7px] text-[12.5px] transition-colors",
                  active === "claude"
                    ? "bg-card text-foreground"
                    : "text-[#e7e5df] hover:bg-card hover:text-foreground",
                )}
              >
                <AgentMark />
                <span className="min-w-0 flex-1 truncate text-left">
                  Claude Code
                </span>
                <StatusDot on={claudeTokenSet === true} />
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
            icon={Container}
            label="Среда агента"
            active={active === "env"}
            onClick={() => go("env")}
            trailing={<StatusDot on={imageCount > 0} />}
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
            icon={Bell}
            label="Уведомления"
            active={active === "ntfy"}
            onClick={() => go("ntfy")}
            trailing={<StatusDot on={ntfyEnabled(ntfy)} />}
          />
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto scroll-smooth">
          {/* key — чтобы смена раздела заново проигрывала вход и сбрасывала локальное
              состояние деталей (подтверждение перевыпуска ключа, «Скопировано»). */}
          <div
            key={active}
            className="mx-auto flex max-w-[680px] animate-[section-in_0.24s_cubic-bezier(0.2,0.8,0.2,1)] flex-col gap-[18px] px-[34px] pt-6 pb-[90px]"
          >
            {active === "claude" && (
              <ClaudeSection
                tokenSet={claudeTokenSet}
                onChange={setClaudeTokenSet}
              />
            )}
            {active === "mcp" && <McpSection onCountChange={setMcpCount} />}
            {active === "env" && (
              <EnvironmentSection onCountChange={setImageCount} />
            )}
            {active === "memory" && (
              <MemorySection
                remote={remote}
                onChange={setRemote}
                onGoSsh={() => go("ssh")}
              />
            )}
            {active === "ssh" && (
              <SshSection publicKey={publicKey} onChange={setPublicKey} />
            )}
            {active === "ntfy" && (
              <NtfySection state={ntfy} onChange={setNtfy} />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

type NtfyState = {
  server: string;
  topic: string;
  tokenSet: boolean;
  events: string[];
};

// Уведомления считаются включёнными, только когда есть куда слать И что слать.
function ntfyEnabled(state: NtfyState | null): boolean {
  return Boolean(state?.topic.trim() && state.events.length > 0);
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
  trailing: ReactNode;
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

// AgentMark — квадратная марка агента в навигации и заголовке раздела. Пока агент один,
// но марка задаётся здесь же, чтобы добавление второго свелось к новому элементу списка.
function AgentMark({ large = false }: { large?: boolean }) {
  return (
    <span
      className={cn(
        "flex shrink-0 items-center justify-center bg-primary font-semibold text-white",
        large
          ? "size-[34px] rounded-[9px] text-[15px]"
          : "size-4 rounded-[5px] text-[9.5px]",
      )}
    >
      C
    </span>
  );
}

// ─── Агенты → Claude Code ─────────────────────────────────────────────────────

function ClaudeSection({
  tokenSet,
  onChange,
}: {
  tokenSet: boolean | null;
  onChange: (v: boolean) => void;
}) {
  const [draft, setDraft] = useState("");
  const [reveal, setReveal] = useState(false);
  const [saving, setSaving] = useState(false);

  const save = useCallback(
    async (token: string) => {
      setSaving(true);
      try {
        const res = await authClient.setClaudeToken({ token });
        onChange(res.tokenSet);
        setDraft("");
        setReveal(false);
        toast.success(res.tokenSet ? "Токен Claude сохранён" : "Токен Claude очищен");
      } catch (err) {
        toast.error(errorText(err, "Не удалось сохранить токен"));
      } finally {
        setSaving(false);
      }
    },
    [onChange],
  );

  if (tokenSet === null) return <Loading />;

  return (
    <>
      <div className="flex items-center gap-1.5 text-[11.5px] text-[#7a776f]">
        Агенты
        <ChevronRight className="size-3" />
        <span className="text-muted-foreground">Claude</span>
      </div>

      <div className="flex flex-col gap-[18px]">
        <div className="flex items-center gap-3">
          <AgentMark large />
          <div className="min-w-0">
            <div className="flex items-center gap-2.5">
              <h2 className="text-[16.5px] font-semibold">Claude Code</h2>
              <Badge on={tokenSet}>
                {tokenSet ? "токен задан" : "токен не задан"}
              </Badge>
            </div>
            {/* Техстрока: видно, что это обычный ACP-агент, а не особая сущность. */}
            <div className="font-mono text-[11.5px] text-[#7a776f]">
              acp · claude-code · подписочный токен
            </div>
          </div>
        </div>

        <Description>
          Подписочный токен Claude Code — создаётся командой{" "}
          <Code>claude setup-token</Code>. Используется для авторизации агента в ваших
          сессиях.
        </Description>

        <div className="flex flex-col gap-2">
          <FieldLabel>{tokenSet ? "Новый токен" : "Токен"}</FieldLabel>
          <div className="flex items-start gap-2">
            <div className="relative flex-1">
              <Input
                type={reveal ? "text" : "password"}
                placeholder="sk-ant-oat01-…"
                autoComplete="off"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                className="h-[41px] bg-[#1c1b1a] pr-10 font-mono text-[12.5px] focus-visible:border-[#5a4034]"
              />
              <button
                type="button"
                onClick={() => setReveal((v) => !v)}
                aria-label={reveal ? "Скрыть токен" : "Показать токен"}
                className="absolute inset-y-0 right-0 flex w-[33px] items-center justify-center rounded-r-md text-muted-foreground transition-colors hover:bg-card hover:text-foreground"
              >
                {reveal ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            </div>
            <Button
              className="h-[41px]"
              disabled={saving || !draft.trim()}
              onClick={() => void save(draft.trim())}
            >
              {saving && <Loader2 className="size-4 animate-spin" />}
              Сохранить
            </Button>
          </div>
          <SecretNote>
            Шифруется на сервере, после сохранения не отображается — только флаг «задан»
          </SecretNote>
        </div>

        {tokenSet && (
          <DangerZone
            title="Отключить агента"
            hint="Сессии с этим агентом перестанут создаваться"
          >
            <Button
              variant="outline"
              disabled={saving}
              onClick={() => void save("")}
            >
              Очистить токен
            </Button>
          </DangerZone>
        )}
      </div>
    </>
  );
}

// ─── Память ───────────────────────────────────────────────────────────────────

function MemorySection({
  remote,
  onChange,
  onGoSsh,
}: {
  remote: string | null;
  onChange: (v: string) => void;
  onGoSsh: () => void;
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
          дописывает в конце. Репозиторий один на пользователя и общий для всех агентов.
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

      <DangerZone
        title="Доступ к репозиторию"
        hint="Для git@-remote используется SSH-ключ агента — отдельный ключ не нужен"
      >
        <Button variant="outline" onClick={onGoSsh}>
          К ключу
        </Button>
      </DangerZone>
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

function NtfySection({
  state,
  onChange,
}: {
  state: NtfyState | null;
  onChange: (v: NtfyState) => void;
}) {
  const [tokenDraft, setTokenDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const [tested, setTested] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    if (!tested) return;
    const timer = setTimeout(() => setTested(false), 2200);
    return () => clearTimeout(timer);
  }, [tested]);

  const save = useCallback(async () => {
    if (!state) return;
    setSaving(true);
    try {
      const res = await authClient.setNtfySettings({
        server: state.server.trim(),
        topic: state.topic.trim(),
        token: tokenDraft,
        events: state.events,
      });
      onChange({
        server: res.server,
        topic: res.topic,
        tokenSet: res.tokenSet,
        events: res.events,
      });
      setTokenDraft("");
      toast.success("Настройки уведомлений сохранены");
    } catch (err) {
      toast.error(errorText(err, "Не удалось сохранить настройки уведомлений"));
    } finally {
      setSaving(false);
    }
  }, [state, tokenDraft, onChange]);

  // Тест шлётся по СОХРАНЁННЫМ настройкам — сервер других не знает. Поэтому ошибку
  // показываем как есть: чаще всего это неверный топик или токен.
  const test = useCallback(async () => {
    setTesting(true);
    try {
      await authClient.testNtfy({});
      setTested(true);
    } catch (err) {
      toast.error(errorText(err, "Не удалось отправить уведомление"));
    } finally {
      setTesting(false);
    }
  }, []);

  if (!state) return <Loading />;

  const patch = (next: Partial<NtfyState>) => onChange({ ...state, ...next });
  const toggle = (key: string) =>
    patch({
      events: state.events.includes(key)
        ? state.events.filter((e) => e !== key)
        : [...state.events, key],
    });

  return (
    <>
      <SectionHeader
        title="Уведомления"
        badge={
          <Badge on={ntfyEnabled(state)}>
            {ntfyEnabled(state) ? "включены" : "выключены"}
          </Badge>
        }
      >
        <Description>
          Персональный push через <ExternalLink href="https://ntfy.sh">ntfy</ExternalLink>
          . Подпишитесь на свой топик в приложении ntfy — и получайте уведомления о
          выбранных событиях сессий.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        <FieldLabel>Топик</FieldLabel>
        <Input
          placeholder="brigade-alerts-a8f3"
          autoComplete="off"
          value={state.topic}
          onChange={(e) => patch({ topic: e.target.value })}
          className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px] focus-visible:border-[#5a4034]"
        />
        <p className="text-[11.5px] leading-[1.55] text-[#6c695f]">
          Кто знает топик — читает ваши уведомления. Придумайте неочевидный.
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3.5">
        <div className="flex flex-col gap-2">
          <FieldLabel>Сервер</FieldLabel>
          <Input
            placeholder="https://ntfy.sh"
            autoComplete="off"
            value={state.server}
            onChange={(e) => patch({ server: e.target.value })}
            className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px] focus-visible:border-[#5a4034]"
          />
          <p className="text-[11.5px] leading-[1.55] text-[#6c695f]">
            Пусто — публичный ntfy.sh
          </p>
        </div>
        <div className="flex flex-col gap-2">
          <FieldLabel>
            {state.tokenSet ? "Новый токен доступа" : "Токен доступа · необязательно"}
          </FieldLabel>
          <Input
            type="password"
            placeholder={state.tokenSet ? "Пусто — не менять" : "tk_…"}
            autoComplete="off"
            value={tokenDraft}
            onChange={(e) => setTokenDraft(e.target.value)}
            className="h-[41px] bg-[#1c1b1a] font-mono text-[12.5px] focus-visible:border-[#5a4034]"
          />
          <SecretNote>Шифруется на сервере, обратно не отдаётся</SecretNote>
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel>События</FieldLabel>
        <div className="divide-y overflow-hidden rounded-[11px] border bg-[#1c1b1a]">
          {NTFY_EVENTS.map((ev) => (
            <button
              key={ev.key}
              type="button"
              onClick={() => toggle(ev.key)}
              className="flex w-full items-center gap-3 px-3.5 py-3 text-left transition-colors hover:bg-[#232221]"
            >
              <span className="min-w-0 flex-1">
                <span className="block text-[13px]">{ev.label}</span>
                <span className="block text-[11.5px] text-[#6c695f]">{ev.hint}</span>
              </span>
              <Toggle on={state.events.includes(ev.key)} />
            </button>
          ))}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <Button
          className="h-[38px]"
          disabled={saving || !state.topic.trim()}
          onClick={() => void save()}
        >
          {saving && <Loader2 className="size-4 animate-spin" />}
          Сохранить
        </Button>
        <Button
          variant="outline"
          className="h-[38px] gap-1.5"
          disabled={testing || !state.topic.trim()}
          onClick={() => void test()}
        >
          {testing ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Send className="size-3.5" />
          )}
          Тестовый push
        </Button>
        {tested && <span className="text-xs text-[#8dbf82]">Отправлено</span>}
      </div>
    </>
  );
}

// Toggle — переключатель события. Своя разметка, а не чекбокс: строка целиком служит
// кнопкой, а переключателю нужен только вид состояния.
function Toggle({ on }: { on: boolean }) {
  return (
    <span
      className={cn(
        "flex h-5 w-9 shrink-0 items-center rounded-full p-0.5 transition-colors duration-200",
        on ? "bg-primary" : "bg-secondary",
      )}
    >
      <span
        className={cn(
          "size-4 rounded-full bg-white transition-transform duration-200 ease-[cubic-bezier(.25,1,.4,1)]",
          on && "translate-x-4",
        )}
      />
    </span>
  );
}
