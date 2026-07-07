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
    </div>
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
