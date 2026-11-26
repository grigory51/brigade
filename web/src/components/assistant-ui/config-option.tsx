"use client";

import { useEffect, useId, useRef, useState } from "react";
import { CheckIcon, ChevronDownIcon, Loader2Icon, ShieldIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { ConfigOption } from "@/features/acp/useAcpRuntime";
import { DockChip } from "@/features/acp/dock/DockChip";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export type ConfigOptionSelectProps = {
  option: ConfigOption;
  onChange: (configId: string, value: string) => void | Promise<void>;
  variant?: "composer" | "dock";
  className?: string;
  disabled?: boolean;
};

const categoryLabels: Record<string, string> = {
  model: "Модель",
  mode: "Права агента",
  thought_level: "Глубина рассуждений",
};

const standardValues: Record<string, Record<string, string>> = {
  thought_level: {
    default: "По умолчанию",
    auto: "Авто",
    none: "Без рассуждений",
    off: "Выключены",
    minimal: "Минимальная",
    low: "Низкая",
    medium: "Средняя",
    high: "Высокая",
    xhigh: "Очень высокая",
    "extra high": "Очень высокая",
    max: "Максимальная",
  },
  mode: {
    default: "По умолчанию",
    ask: "Запрашивать разрешение",
    "ask me": "С подтверждением",
    "ask before edits": "С подтверждением",
    "approve for me": "Автоодобрение",
    auto: "Авто",
    plan: "Планирование",
    "accept edits": "Разрешать изменения",
    acceptedits: "Разрешать изменения",
    "auto approve": "Автоодобрение",
    "auto-approve": "Автоодобрение",
    "bypass permissions": "Без запросов разрешений",
    bypasspermissions: "Без запросов разрешений",
    "full access": "Полный доступ",
    "read only": "Только чтение",
    "read-only": "Только чтение",
  },
};

// Подписи переводятся только для известных категорий; идентификаторы остаются неизменными.
function valueLabel(option: ConfigOption, name: string): string {
  return standardValues[option.category ?? ""]?.[name.toLowerCase()] ?? name;
}

export function ConfigOptionSelect(props: ConfigOptionSelectProps) {
  // При смене опции состояние предыдущего запроса не должно блокировать новый селектор.
  return <ConfigOptionSelectControl key={props.option.id} {...props} />;
}

function ConfigOptionSelectControl({
  option,
  onChange,
  variant = "composer",
  className,
  disabled = false,
}: ConfigOptionSelectProps) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef(false);
  const mounted = useRef(true);
  const errorId = useId();
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const label = categoryLabels[option.category ?? ""] ?? option.name;
  const current = option.options.find((value) => value.value === option.currentValue);
  const currentLabel = valueLabel(option, current?.name ?? option.currentValue);
  const accessibleLabel = `${label}: ${currentLabel}`;
  const TriggerButton = variant === "dock" ? DockChip : Button;

  async function select(value: string): Promise<void> {
    if (disabled || inFlight.current || value === option.currentValue) return;
    inFlight.current = true;
    setPending(true);
    setError(null);
    try {
      await onChange(option.id, value);
    } catch (cause) {
      if (mounted.current) setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      inFlight.current = false;
      if (mounted.current) setPending(false);
    }
  }

  return (
    <div className="relative min-w-0">
      <DropdownMenu>
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <TriggerButton
                  type="button"
                  {...(variant === "dock" ? {} : { variant: "ghost" as const, size: "sm" as const })}
                  disabled={disabled || pending || option.options.length === 0}
                  aria-label={accessibleLabel}
                  title={accessibleLabel}
                  aria-busy={pending}
                  aria-describedby={error ? errorId : undefined}
                  className={cn(
                    "max-w-full",
                    variant === "composer" && "text-foreground h-7 gap-1 px-2 text-xs font-normal",
                    className,
                  )}
                >
                  {variant === "dock" && option.category === "mode" && <ShieldIcon aria-hidden className="size-3.5 shrink-0" />}
                  <span className={cn("truncate", variant === "dock" && option.category === "mode" && "hidden sm:inline")}>{currentLabel}</span>
                  {pending
                    ? <Loader2Icon aria-hidden className="size-3 shrink-0 animate-spin" />
                    : <ChevronDownIcon aria-hidden className="size-3 shrink-0" />}
                </TriggerButton>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent className="pointer-events-none">{accessibleLabel}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <DropdownMenuContent side="top" align="start" className="w-64" aria-label={label}>
          <DropdownMenuLabel>{label}</DropdownMenuLabel>
          {option.options.map((value) => (
            <DropdownMenuItem
              key={value.value}
              disabled={disabled || pending}
              onSelect={() => { void select(value.value); }}
              className="flex-col items-start gap-0.5"
            >
              <span className="flex w-full items-center justify-between gap-2 text-sm">
                {valueLabel(option, value.name)}
                {value.value === option.currentValue && <CheckIcon aria-hidden className="size-3.5 shrink-0" />}
              </span>
              {value.description && <span className="text-muted-foreground text-xs">{value.description}</span>}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      {pending && <span role="status" className="sr-only">{label}: сохранение…</span>}
      {error && <p id={errorId} role="alert" className="text-destructive mt-1 max-w-64 text-xs">Не удалось изменить настройку: {error}</p>}
    </div>
  );
}
