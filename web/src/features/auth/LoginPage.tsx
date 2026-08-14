import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { Loader2 } from "lucide-react";
import { useAuth } from "./AuthContext";
import { authClient, desktopClient } from "@/api/client";
import type { AuthMethod } from "@/api/gen/brigade/v1/auth_pb";
import type { DesktopEnvironment } from "@/api/gen/brigade/v1/desktop_pb";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function LoginPage() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [methods, setMethods] = useState<AuthMethod[] | null>(null);
  const [remoteEnvironmentId, setRemoteEnvironmentId] = useState("");
  const [environments, setEnvironments] = useState<DesktopEnvironment[] | null>(null);

  // Куда вернуть после логина: исходный защищённый маршрут или список сессий.
  const from =
    (location.state as { from?: string } | null)?.from ?? "/sessions";

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const desktop = await desktopClient.listEnvironments({});
        if (!cancelled) setEnvironments(desktop.environments);
        const remote = desktop.environments.find(
          (environment) => environment.active && environment.kind === "remote",
        );
        if (remote) {
          if (!cancelled) {
            setRemoteEnvironmentId(remote.id);
            setMethods(remote.authMethods);
            if (remote.error) setError(remote.error);
          }
          return;
        }
      } catch {
        // Обычный web-инстанс не предоставляет DesktopService.
      }
      const info = await authClient.getServerInfo({});
      if (!cancelled) setMethods(info.authMethods);
    };
    void load().catch(() => {
      if (!cancelled) {
        setMethods([]);
        setError("Не удалось получить способы входа.");
      }
    });
    if (new URLSearchParams(location.search).has("error")) {
      setError("Не удалось войти через OIDC.");
    }
    return () => {
      cancelled = true;
    };
  }, [location.search]);

  const startOIDC = () => {
    const path = remoteEnvironmentId ? "/desktop/oidc/start" : "/auth/oidc/start";
    const query = new URLSearchParams({ return_to: from });
    if (remoteEnvironmentId) query.set("environment_id", remoteEnvironmentId);
    window.location.assign(`${path}?${query}`);
  };

  const passwordEnabled =
    methods?.some((method) => method.kind === "password") ?? false;
  const oidcMethods = methods?.filter((method) => method.kind === "oidc") ?? [];
  const activeEnvironment = environments?.find((environment) => environment.active);

  const selectEnvironment = async (id: string) => {
    const environment = await desktopClient.selectEnvironment({ id });
    window.location.assign(environment.connected ? "/sessions" : "/login");
  };

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await login(username.trim(), password);
      navigate(from, { replace: true });
    } catch (err) {
      setError(
        err instanceof ConnectError
          ? "Неверный логин или пароль"
          : "Не удалось войти. Проверьте соединение.",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative flex min-h-full items-center justify-center p-6">
      {/* Мягкое свечение акцента сверху для глубины фона. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 h-72 bg-[radial-gradient(60%_60%_at_50%_0%,oklch(0.65_0.16_256/0.12),transparent)]"
      />
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="flex items-center gap-3">
            <img src="/logo.svg" alt="brigade" className="size-11 rounded-xl" />
            <div className="space-y-0.5">
              <CardTitle className="text-xl">brigade</CardTitle>
              <CardDescription>Запуск кодинг-агентов на VPC</CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {environments && activeEnvironment && (
            <div className="mb-5 space-y-2">
              <Label htmlFor="environment">Окружение</Label>
              <select
                id="environment"
                value={activeEnvironment.id}
                onChange={(event) => void selectEnvironment(event.target.value)}
                className="border-input bg-background h-9 w-full rounded-md border px-3 text-sm outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                {environments.map((environment) => (
                  <option key={environment.id} value={environment.id}>
                    {environment.name}
                  </option>
                ))}
              </select>
              {activeEnvironment.kind === "remote" && (
                <p className="truncate text-xs text-muted-foreground" title={activeEnvironment.baseUrl}>
                  {activeEnvironment.baseUrl}
                </p>
              )}
            </div>
          )}
          {methods === null ? (
            <div className="flex h-10 items-center justify-center">
              <Loader2 className="size-4 animate-spin" />
            </div>
          ) : (
            <div className="space-y-4">
              {oidcMethods.map((method) => (
                <Button
                  key={method.id}
                  type="button"
                  className="w-full"
                  onClick={startOIDC}
                >
                  Войти через {method.name}
                </Button>
              ))}
              {passwordEnabled && (
                <form onSubmit={onSubmit} className="space-y-4">
                  <div className="space-y-2">
                    <Label htmlFor="username">Логин</Label>
                    <Input
                      id="username"
                      autoComplete="username"
                      autoFocus
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      required
                    />
                  </div>

                  <div className="space-y-2">
                    <Label htmlFor="password">Пароль</Label>
                    <Input
                      id="password"
                      type="password"
                      autoComplete="current-password"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      required
                    />
                  </div>

                  {error && (
                    <p role="alert" className="text-sm text-destructive">
                      {error}
                    </p>
                  )}

                  <Button
                    type="submit"
                    className="w-full"
                    disabled={busy || !username || !password}
                  >
                    {busy ? (
                      <Loader2 className="size-4 animate-spin" />
                    ) : (
                      "Войти"
                    )}
                  </Button>
                </form>
              )}
              {!passwordEnabled && error && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
              {methods.length === 0 && !error && (
                <p role="alert" className="text-sm text-destructive">
                  На сервере не настроен способ входа.
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
