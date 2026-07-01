import { Loader2 } from "lucide-react";

// Нейтральный полноэкранный индикатор для интервалов, когда роутер ещё не может
// принять решение о редиректе (первичная проверка сессии). Вынесен отдельно,
// чтобы не дублировать разметку в гейтах маршрутов.
export function RouteSpinner() {
  return (
    <div className="flex h-full items-center justify-center">
      <Loader2 className="size-6 animate-spin text-muted-foreground" />
    </div>
  );
}
