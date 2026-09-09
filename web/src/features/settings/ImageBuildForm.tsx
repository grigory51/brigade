import { useEffect, useId, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { authClient } from "@/api/client";
import type { AgentImageBuild, StartAgentImageBuildRequest } from "@/api/gen/brigade/v1/auth_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { errorText } from "./ui";

const STATUS_LABELS: Record<string, string> = {
  queued: "Сборка в очереди",
  building: "Образ собирается",
  succeeded: "Образ собран",
  failed: "Сборка завершилась с ошибкой",
  cancelled: "Сборка отменена",
  interrupted: "Сборка прервана. Можно запустить её снова.",
};

type Action = "get" | "start" | "cancel";
type StartInput = Pick<StartAgentImageBuildRequest, "name" | "script" | "noCache">;

function isActive(build: AgentImageBuild | null): boolean {
  return build?.status === "queued" || build?.status === "building";
}

export function ImageBuildForm({
  disabled,
  onBusyChange,
  onSucceeded,
}: {
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onSucceeded: (signal: AbortSignal) => Promise<void>;
}) {
  const id = useId();
  const [name, setName] = useState("");
  const [script, setScript] = useState("");
  const [build, setBuild] = useState<AgentImageBuild | null>(null);
  const [checking, setChecking] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const dirty = useRef(false);
  const request = useRef<(action: Action, input?: StartInput) => void>(
    () => {},
  );

  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pending: Promise<void> = Promise.resolve();
    let pollController: AbortController | undefined;
    let reading = false;
    let actionPending = false;
    let currentBuild: AgentImageBuild | null = null;
    let refreshedId: string | undefined;
    let uncertain = true;

    // Мутации сериализованы; отмена прерывает чтение, чтобы не ждать ответа сети.
    const run = (action: Action, input?: StartInput) => {
      if (disposed || actionPending || (action === "get" && reading)) return;
      clearTimeout(timer);
      if (action === "get") {
        reading = true;
        setChecking(true);
      } else {
        actionPending = true;
        setSubmitting(true);
        onBusyChange(true);
        if (action === "cancel") pollController?.abort();
      }
      setError("");
      pending = pending.then(async () => {
        if (disposed) return;
        const requestController = new AbortController();
        const abortRequest = () => requestController.abort();
        signal.addEventListener("abort", abortRequest, { once: true });
        const options = { signal: requestController.signal, timeoutMs: 15_000 };
        if (action === "get") pollController = requestController;
        let succeeded = false;
        try {
          let res: AgentImageBuild;
          if (action === "start") {
            if (!input || uncertain || isActive(currentBuild)) return;
            res = await authClient.startAgentImageBuild(input, options);
          } else if (action === "cancel") {
            if (!currentBuild || !isActive(currentBuild)) return;
            res = await authClient.cancelAgentImageBuild({ id: currentBuild.id }, options);
          } else {
            res = await authClient.getAgentImageBuild({}, options);
          }
          if (disposed || requestController.signal.aborted) return;
          currentBuild = res;
          uncertain = false;
          setBuild(res);
          setError("");
          if (!dirty.current) {
            setName(res.name);
            setScript(res.script);
          }
          if (res.status === "succeeded" && refreshedId !== res.id) {
            try {
              await onSucceeded(signal);
            } catch (err) {
              if (!disposed) {
                uncertain = true;
                setError(errorText(err, "Образ собран, но не удалось обновить список и квоту. Повторите запрос."));
              }
              return;
            }
            if (disposed) return;
            refreshedId = res.id;
          }
          succeeded = true;
        } catch (err) {
          if (disposed || requestController.signal.aborted) return;
          // После сетевой ошибки результат мутации неизвестен до повторного чтения.
          uncertain = true;
          const message = action === "start"
            ? "Не удалось запустить сборку. Проверьте её состояние повторным запросом."
            : action === "cancel"
              ? "Не удалось отменить сборку. Проверьте её состояние повторным запросом."
              : "Не удалось получить состояние сборки. Повторите запрос.";
          setError(errorText(err, message));
        } finally {
          signal.removeEventListener("abort", abortRequest);
          if (pollController === requestController) pollController = undefined;
          if (!disposed) {
            if (action === "get") {
              reading = false;
              setChecking(false);
            } else {
              actionPending = false;
              setSubmitting(false);
            }
            onBusyChange(uncertain || isActive(currentBuild) || actionPending);
            if (succeeded && isActive(currentBuild) && !actionPending) {
              timer = setTimeout(() => run("get"), 1000);
            }
          }
        }
      });
    };

    request.current = run;
    onBusyChange(true);
    run("get");
    return () => {
      disposed = true;
      clearTimeout(timer);
      controller.abort();
      onBusyChange(false);
    };
  }, [onBusyChange, onSucceeded]);

  const active = isActive(build);
  const locked = disabled || submitting || active || checking || build === null || !!error;

  return (
    <div className="flex min-w-0 flex-col gap-2 border-t pt-[18px]">
      <h3 className="text-[14px] font-semibold">Собрать образ из скрипта</h3>
      <label htmlFor={`${id}-name`} className="text-xs text-muted-foreground">Название</label>
      <Input
        id={`${id}-name`}
        aria-describedby={`${id}-name-description`}
        value={name}
        onChange={(event) => {
          dirty.current = true;
          setName(event.target.value);
        }}
        placeholder="python-tools"
        required
        maxLength={40}
        disabled={locked}
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        className="h-[41px] bg-background font-mono"
      />
      <p id={`${id}-name-description`} className="text-[12.5px] leading-[1.65] text-muted-foreground">
        Обязательное поле. До 40 символов: a-z, 0-9, точка (.), подчёркивание (_) и дефис (-).
        Начинается с буквы или цифры.
      </p>
      <label htmlFor={id} className="text-xs text-muted-foreground">Скрипт установки</label>
      <p id={`${id}-description`} className="text-[12.5px] leading-[1.65] text-muted-foreground">
        Укажите shell-команды для подготовки окружения агента. После успешной сборки
        образ появится в списке выше и будет доступен для новых сессий.
        <br />
        Скрипт выполняется от root при сборке. Не добавляйте токены и пароли: скрипт и лог сохраняются.
      </p>
      <Textarea
        id={id}
        aria-describedby={`${id}-description`}
        value={script}
        onChange={(event) => {
          dirty.current = true;
          setScript(event.target.value);
        }}
        readOnly={locked}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        className="field-sizing-fixed min-h-40 max-h-80 resize-y overflow-auto bg-background font-mono"
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          disabled={locked || !name.trim() || !script.trim()}
          onClick={() => request.current("start", { name: name.trim(), script, noCache: false })}
        >
          Собрать
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={locked || !name.trim() || !script.trim()}
          onClick={() => request.current("start", { name: name.trim(), script, noCache: true })}
        >
          Пересобрать без кеша
        </Button>
        {active && (
          <Button
            type="button"
            variant="outline"
            disabled={disabled || submitting}
            onClick={() => request.current("cancel")}
          >
            Отменить
          </Button>
        )}
      </div>
      <p role="status" className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
        {(active || submitting || (checking && !build)) && (
          <Loader2 aria-hidden="true" className="size-4 shrink-0 animate-spin motion-reduce:animate-none" />
        )}
        {submitting ? "Отправляем запрос…"
          : checking && !build ? "Загружаем состояние сборки…"
            : build ? STATUS_LABELS[build.status] ?? (build.status || "Сборок пока нет")
              : "Состояние сборки неизвестно"}
      </p>
      {error && (
        <div className="flex flex-wrap items-center gap-2">
          <p role="alert" className="min-w-0 break-words text-[12.5px] text-destructive">{error}</p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={checking || submitting}
            onClick={() => request.current("get")}
          >
            {checking ? "Проверяем…" : "Повторить запрос"}
          </Button>
        </div>
      )}
      {build?.error && (
        <p className="whitespace-pre-wrap break-words text-[12.5px] text-destructive">{build.error}</p>
      )}
      {build?.status === "succeeded" && (build.name || build.image) && (
        <p className="break-all font-mono text-[12.5px]">{build.name || build.image}</p>
      )}
      {build?.log && (
        <div className="flex min-w-0 flex-col gap-1.5">
          <span id={`${id}-log`} className="text-xs text-muted-foreground">Лог сборки</span>
          <pre
            aria-labelledby={`${id}-log`}
            tabIndex={0}
            className="max-h-64 overflow-auto rounded-[10px] border bg-background px-3 py-2.5 font-mono text-[11.5px] leading-[1.7] focus-visible:outline-ring"
          >
            {build.log}
          </pre>
        </div>
      )}
    </div>
  );
}
