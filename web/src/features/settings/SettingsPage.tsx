import { useCallback, useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Check, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { authClient } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

/**
 * SettingsPage — персональные настройки пользователя. Раздел Claude: подписочный
 * токен Claude Code. После сохранения значение не показывается — сервер отдаёт
 * только флаг «токен задан».
 */
export function SettingsPage() {
  const [tokenSet, setTokenSet] = useState<boolean | null>(null); // null = загрузка
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await authClient.getClaudeSettings({});
      setTokenSet(res.tokenSet);
    } catch {
      setTokenSet(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const res = await authClient.setClaudeToken({ token: draft.trim() });
      setTokenSet(res.tokenSet);
      setDraft(""); // токен из UI сразу убираем — он больше не показывается
      toast.success(
        res.tokenSet ? "Токен Claude сохранён" : "Токен Claude очищен",
      );
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось сохранить токен",
      );
    } finally {
      setSaving(false);
    }
  }, [draft]);

  return (
    <div className="mx-auto w-full max-w-2xl p-6">
      <h1 className="mb-4 text-lg font-semibold">Настройки</h1>

      <Card>
        <CardHeader>
          <CardTitle>Claude</CardTitle>
          <CardDescription>
            Подписочный токен Claude Code (создаётся командой{" "}
            <code className="text-xs">claude setup-token</code>). Используется для
            авторизации агентов в ваших сессиях. После сохранения токен не
            отображается.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {tokenSet === null ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Загрузка…
            </div>
          ) : (
            <div className="flex items-center gap-1.5 text-sm">
              {tokenSet ? (
                <>
                  <Check className="size-4 text-success" />
                  <span className="text-muted-foreground">Токен задан</span>
                </>
              ) : (
                <span className="text-muted-foreground">Токен не задан</span>
              )}
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="claude-token">
              {tokenSet ? "Новый токен" : "Токен"}
            </Label>
            <Input
              id="claude-token"
              type="password"
              placeholder="sk-ant-oat01-…"
              autoComplete="off"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
          </div>

          <div className="flex items-center gap-2">
            <Button onClick={() => void save()} disabled={saving || !draft.trim()}>
              {saving && <Loader2 className="size-4 animate-spin" />}
              Сохранить
            </Button>
            {tokenSet && (
              <Button
                variant="outline"
                disabled={saving}
                onClick={() => {
                  setDraft("");
                  void authClient
                    .setClaudeToken({ token: "" })
                    .then((res) => {
                      setTokenSet(res.tokenSet);
                      toast.success("Токен Claude очищен");
                    })
                    .catch(() => toast.error("Не удалось очистить токен"));
                }}
              >
                Очистить
              </Button>
            )}
          </div>
        </CardContent>
      </Card>

      <MemoryCard />
      <NtfyCard />
    </div>
  );
}

/** Событие уведомления: ключ совпадает с backend (internal/notify). */
const NTFY_EVENTS: { key: string; label: string }[] = [
  { key: "turn_end", label: "Агент завершил ответ" },
  { key: "error", label: "Ошибка в turn'е" },
];

/**
 * NtfyCard — раздел «Уведомления»: персональный push через ntfy. Пользователь задаёт топик
 * (обязателен), опционально свой сервер и токен доступа, и выбирает события. Токен на сервере
 * шифруется и наружу не отдаётся (только флаг «задан»); пустой при сохранении оставляет прежний.
 */
function NtfyCard() {
  const [loaded, setLoaded] = useState(false);
  const [server, setServer] = useState("");
  const [topic, setTopic] = useState("");
  const [tokenSet, setTokenSet] = useState(false);
  const [tokenDraft, setTokenDraft] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let alive = true;
    authClient
      .getNtfySettings({})
      .then((r) => {
        if (!alive) return;
        setServer(r.server);
        setTopic(r.topic);
        setTokenSet(r.tokenSet);
        setEvents(r.events);
      })
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  const toggleEvent = useCallback((key: string) => {
    setEvents((prev) =>
      prev.includes(key) ? prev.filter((e) => e !== key) : [...prev, key],
    );
  }, []);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const res = await authClient.setNtfySettings({
        server: server.trim(),
        topic: topic.trim(),
        token: tokenDraft,
        events,
      });
      setServer(res.server);
      setTopic(res.topic);
      setTokenSet(res.tokenSet);
      setEvents(res.events);
      setTokenDraft(""); // токен из UI сразу убираем — он больше не показывается
      toast.success("Настройки уведомлений сохранены");
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось сохранить настройки уведомлений",
      );
    } finally {
      setSaving(false);
    }
  }, [server, topic, tokenDraft, events]);

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>Уведомления</CardTitle>
        <CardDescription>
          Персональный push через <code className="text-xs">ntfy</code> (
          <a
            href="https://ntfy.sh"
            target="_blank"
            rel="noreferrer"
            className="underline"
          >
            ntfy.sh
          </a>{" "}
          или свой сервер). Укажите топик, подпишитесь на него в приложении ntfy —
          и получайте пуш о выбранных событиях сессий. Токен нужен только для
          защищённых топиков; после сохранения он не отображается.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {!loaded ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            Загрузка…
          </div>
        ) : (
          <>
            <div className="space-y-2">
              <Label htmlFor="ntfy-topic">Топик</Label>
              <Input
                id="ntfy-topic"
                placeholder="brigade-alerts-a8f3"
                autoComplete="off"
                value={topic}
                onChange={(e) => setTopic(e.target.value)}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="ntfy-server">Сервер (необязательно)</Label>
              <Input
                id="ntfy-server"
                placeholder="https://ntfy.sh"
                autoComplete="off"
                value={server}
                onChange={(e) => setServer(e.target.value)}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="ntfy-token">
                {tokenSet ? "Новый токен доступа" : "Токен доступа (необязательно)"}
              </Label>
              <Input
                id="ntfy-token"
                type="password"
                placeholder={
                  tokenSet ? "Оставьте пустым, чтобы не менять" : "tk_…"
                }
                autoComplete="off"
                value={tokenDraft}
                onChange={(e) => setTokenDraft(e.target.value)}
              />
            </div>

            <div className="space-y-2">
              <Label>События</Label>
              <div className="space-y-1.5">
                {NTFY_EVENTS.map((ev) => (
                  <label
                    key={ev.key}
                    className="flex items-center gap-2 text-sm text-muted-foreground"
                  >
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={events.includes(ev.key)}
                      onChange={() => toggleEvent(ev.key)}
                    />
                    {ev.label}
                  </label>
                ))}
              </div>
            </div>

            <Button
              onClick={() => void save()}
              disabled={saving || !topic.trim()}
            >
              {saving && <Loader2 className="size-4 animate-spin" />}
              Сохранить
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * MemoryCard — раздел «Память»: приватный git-репозиторий заметок пользователя и SSH-ключ
 * к нему. Репозиторий и ключ ПЕР-ЮЗЕРНЫЕ; ключ на сервере шифруется и наружу не отдаётся
 * (только флаг «задан»). Пустой ключ при сохранении оставляет прежний.
 */
function MemoryCard() {
  const [keySet, setKeySet] = useState<boolean | null>(null); // null = загрузка
  const [remote, setRemote] = useState("");
  const [keyDraft, setKeyDraft] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let alive = true;
    authClient
      .getMemorySettings({})
      .then((r) => {
        if (!alive) return;
        setRemote(r.remote);
        setKeySet(r.keySet);
      })
      .catch(() => {
        if (alive) setKeySet(false);
      });
    return () => {
      alive = false;
    };
  }, []);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const res = await authClient.setMemorySettings({
        remote: remote.trim(),
        sshKey: keyDraft,
      });
      setRemote(res.remote);
      setKeySet(res.keySet);
      setKeyDraft(""); // ключ из UI сразу убираем — он больше не показывается
      toast.success("Настройки памяти сохранены");
    } catch (err) {
      toast.error(
        err instanceof ConnectError
          ? err.rawMessage
          : "Не удалось сохранить настройки памяти",
      );
    } finally {
      setSaving(false);
    }
  }, [remote, keyDraft]);

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>Память</CardTitle>
        <CardDescription>
          Ваш приватный git-репозиторий заметок и SSH-ключ к нему (для{" "}
          <code className="text-xs">git@</code>-remote). Данные изолированы: репозиторий и
          доступ у каждого пользователя свои. Ключ после сохранения не отображается.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {keySet === null ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            Загрузка…
          </div>
        ) : (
          <div className="flex items-center gap-1.5 text-sm">
            {keySet ? (
              <>
                <Check className="size-4 text-success" />
                <span className="text-muted-foreground">SSH-ключ задан</span>
              </>
            ) : (
              <span className="text-muted-foreground">SSH-ключ не задан</span>
            )}
          </div>
        )}

        <div className="space-y-2">
          <Label htmlFor="memory-remote">Git-remote</Label>
          <Input
            id="memory-remote"
            placeholder="git@gitlab.com:you/brigade-memory.git"
            autoComplete="off"
            value={remote}
            onChange={(e) => setRemote(e.target.value)}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="memory-key">
            {keySet ? "Новый SSH-ключ" : "SSH-ключ (приватный, без пароля)"}
          </Label>
          <Textarea
            id="memory-key"
            rows={4}
            placeholder={
              keySet
                ? "Оставьте пустым, чтобы не менять"
                : "-----BEGIN OPENSSH PRIVATE KEY-----"
            }
            autoComplete="off"
            className="font-mono text-xs"
            value={keyDraft}
            onChange={(e) => setKeyDraft(e.target.value)}
          />
        </div>

        <Button
          onClick={() => void save()}
          disabled={saving || (!remote.trim() && !keyDraft.trim())}
        >
          {saving && <Loader2 className="size-4 animate-spin" />}
          Сохранить
        </Button>
      </CardContent>
    </Card>
  );
}
