import { useCallback, useEffect, useState } from "react";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { authClient } from "@/api/client";
import type {
  AgentImagesSettings,
  AgentRuntimeSettings,
} from "@/api/gen/brigade/v1/auth_pb";
import { cn } from "@/lib/utils";
import { ImageBuildForm } from "./ImageBuildForm";
import {
  Badge,
  Code,
  Description,
  ExternalLink,
  FieldLabel,
  Loading,
  SectionHeader,
  errorText,
} from "./ui";

/**
 * Раздел «Среда агента»: где исполняются сессии (local или docker) и — для docker — на
 * каких образах.
 *
 * Режим — свойство инсталляции: в десктопном приложении он правится здесь и применяется
 * перезапуском, в серверном задан конфигом и только показывается. Всё, что ниже режима,
 * относится к контейнерам и в local-режиме не показывается.
 *
 * Пользователь добавляет инструменты скриптом сборки поверх базового образа инстанса.
 */

export function EnvironmentSection() {
  const [settings, setSettings] = useState<AgentImagesSettings | null>(null);
  const [runtime, setRuntime] = useState<AgentRuntimeSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [buildBusy, setBuildBusy] = useState(true);

  const refreshImages = useCallback(async (signal: AbortSignal) => {
    const res = await authClient.getAgentImages({}, { signal, timeoutMs: 15_000 });
    if (!signal.aborted) setSettings(res);
  }, []);

  useEffect(() => {
    let alive = true;
    authClient
      .getAgentImages({})
      .then((res) => {
        if (!alive) return;
        setSettings(res);
      })
      .catch(() => alive && setSettings(null));
    authClient
      .getAgentRuntime({})
      .then((res) => alive && setRuntime(res))
      .catch(() => alive && setRuntime(null));
    return () => {
      alive = false;
    };
  }, []);

  // Режим и контекст применяются при следующем запуске: спавнер создаётся один раз на
  // старте процесса.
  const saveRuntime = useCallback(
    async (mode: string, dockerContext: string) => {
      setBusy(true);
      try {
        setRuntime(await authClient.setAgentRuntime({ mode, dockerContext }));
      } catch (err) {
        toast.error(errorText(err, "Не удалось сохранить режим"));
      } finally {
        setBusy(false);
      }
    },
    [],
  );

  // Удаление передаёт серверу оставшийся список образов.
  const save = useCallback(
    async (images: string[]) => {
      setBusy(true);
      try {
        const res = await authClient.setAgentImages({ images });
        setSettings(res);
      } catch (err) {
        toast.error(errorText(err, "Не удалось сохранить образы"));
      } finally {
        setBusy(false);
      }
    },
    [],
  );

  if (settings === null || runtime === null) return <Loading />;

  const refs = settings.images.map((i) => i.image);
  const used = Number(settings.usedBytes);
  const quota = Number(settings.quotaBytes);
  const ratio = quota > 0 ? Math.min(1, used / quota) : 0;
  const docker = runtime.mode === "docker";
  const canBuild = docker && runtime.runningMode === "docker" && !runtime.restartRequired;
  const controlsBusy = busy || (canBuild && buildBusy);

  return (
    <>
      <SectionHeader title="Среда агента" />

      <div className="flex flex-col gap-2">
        {runtime.editable && <FieldLabel>Режим</FieldLabel>}
        {runtime.editable ? (
          <div className="flex w-fit gap-0.5 rounded-[9px] border p-0.5">
            {(["local", "docker"] as const).map((mode) => (
              <button
                key={mode}
                type="button"
                disabled={controlsBusy}
                onClick={() => void saveRuntime(mode, runtime.dockerContext)}
                className={cn(
                  "rounded-[7px] px-3.5 py-1.5 text-[12.5px] transition-colors disabled:opacity-60",
                  runtime.mode === mode
                    ? "bg-card text-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {mode === "local" ? "Local" : "Docker"}
              </button>
            ))}
          </div>
        ) : (
          <p className="text-[12.5px] text-muted-foreground">
            <span className="text-foreground">{docker ? "Docker" : "Local"}</span>
            {" · задан администратором"}
          </p>
        )}
      </div>

      {/* Docker может быть не установлен вовсе — на свежей машине это обычное дело.
          Показываем это до переключения, а не сломанной сессией после перезапуска. */}
      {runtime.editable && runtime.dockerError && (
        <div className="flex flex-col gap-1.5 rounded-[10px] border border-[#5a4034] bg-[#2a201c] px-3 py-2.5">
          <p className="text-[12.5px] leading-[1.6]">
            {docker && runtime.runningMode === "local"
              ? "Docker недоступен — сессии идут локально."
              : "Docker недоступен."}{" "}
            Установите{" "}
            <ExternalLink href="https://orbstack.dev">OrbStack</ExternalLink> или{" "}
            <ExternalLink href="https://www.docker.com/products/docker-desktop/">
              Docker Desktop
            </ExternalLink>{" "}
            и запустите его.
          </p>
          <p className="font-mono text-[11px] break-all text-[#6c695f]">
            {runtime.dockerError}
          </p>
        </div>
      )}

      {runtime.restartRequired && !runtime.dockerError && (
        <p className="rounded-[10px] border border-[#5a4034] bg-[#2a201c] px-3 py-2.5 text-[12.5px] leading-[1.6]">
          Сохранено. Перезапустите приложение — пока сессии идут в режиме{" "}
          <Code>{runtime.runningMode}</Code>.
        </p>
      )}

      {runtime.editable && docker && (
        <div className="flex flex-col gap-2">
          <FieldLabel>Docker-контекст</FieldLabel>
          <div className="flex flex-col gap-1">
            {runtime.contexts.map((ctx) => (
              <label
                key={ctx.name}
                className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors hover:bg-card"
              >
                <input
                  type="radio"
                  name="docker-context"
                  className="size-3.5 accent-primary"
                  checked={(runtime.dockerContext || "default") === ctx.name}
                  disabled={controlsBusy}
                  onChange={() => void saveRuntime("docker", ctx.name)}
                />
                <span className="min-w-0 flex-1 truncate text-[12.5px]">
                  {ctx.name}
                  {ctx.current && (
                    <span className="ml-1.5 text-[11px] text-muted-foreground">
                      текущий в docker CLI
                    </span>
                  )}
                </span>
                <span className="max-w-[45%] shrink-0 truncate font-mono text-[11px] text-[#6c695f]">
                  {ctx.host}
                </span>
              </label>
            ))}
          </div>
        </div>
      )}

      {/* Всё про контейнеры имеет смысл только в docker-режиме. */}
      {docker && (
        <>
          <div className="flex flex-col gap-2 border-t pt-[18px]">
            <div className="flex items-center gap-2.5">
              <h3 className="text-[14px] font-semibold">Образы</h3>
              <Badge on={refs.length > 0}>
                {refs.length > 0 ? `${refs.length} шт.` : "только базовый"}
              </Badge>
            </div>
            <Description>
              Добавьте инструменты для агента и MCP-серверов скриптом установки.
              Он создаст дополнительный слой поверх базового образа —{" "}
              <Code>{settings.defaultImage}</Code>. Готовое окружение можно выбрать
              при создании сессии.
            </Description>
          </div>

          <div className="flex flex-col gap-2">
            {settings.images.map((img) => (
              <div
                key={img.image}
                className="flex items-center gap-3 rounded-[10px] border bg-card px-3 py-2.5"
              >
                <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">
                  {img.name || img.image}
                </span>
                <span className="shrink-0 text-[11.5px] text-[#6c695f]">
                  {formatBytes(Number(img.sizeBytes))}
                </span>
                <button
                  type="button"
                  aria-label="Удалить"
                  disabled={controlsBusy}
                  onClick={() => void save(refs.filter((ref) => ref !== img.image))}
                  className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-destructive"
                >
                  <Trash2 className="size-3.5" />
                </button>
              </div>
            ))}
            {refs.length === 0 && (
              <p className="text-[12.5px] text-muted-foreground/70">
                Пока пусто — все сессии идут на базовом образе.
              </p>
            )}
          </div>

          {quota > 0 && (
            <div className="flex flex-col gap-1.5">
              <div className="h-1.5 overflow-hidden rounded-full bg-secondary">
                <div
                  className="h-full rounded-full bg-primary transition-[width] duration-300"
                  style={{ width: `${ratio * 100}%` }}
                />
              </div>
              <span className="text-[11.5px] text-[#6c695f]">
                занято {formatBytes(used)} из {formatBytes(quota)}
              </span>
            </div>
          )}

          {canBuild ? (
            <ImageBuildForm
              disabled={busy}
              onBusyChange={setBuildBusy}
              onSucceeded={refreshImages}
            />
          ) : (
            <Description>
              Сборка образа из скрипта будет доступна после перезапуска в режиме Docker.
            </Description>
          )}

        </>
      )}
    </>
  );
}

// formatBytes — вес образа человекочитаемо.
function formatBytes(n: number): string {
  if (n <= 0) return "—";
  const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}
