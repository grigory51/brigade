import { useState, type FormEvent } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { Loader2 } from "lucide-react";
import { useAuth } from "./AuthContext";
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

  // Куда вернуть после логина: исходный защищённый маршрут или список сессий.
  const from =
    (location.state as { from?: string } | null)?.from ?? "/sessions";

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
            <div className="flex size-11 items-center justify-center rounded-xl bg-primary text-xl font-bold text-primary-foreground">
              b
            </div>
            <div className="space-y-0.5">
              <CardTitle className="text-xl">brigade</CardTitle>
              <CardDescription>Запуск кодинг-агентов на VPC</CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
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
              {busy ? <Loader2 className="size-4 animate-spin" /> : "Войти"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
