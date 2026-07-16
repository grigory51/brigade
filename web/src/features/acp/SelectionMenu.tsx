import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Quote } from "lucide-react";

import { usePendingContext } from "@/components/assistant-ui/composer-context";

// SelectionMenu — плавающее меню на выделении текста в сообщениях ACP-чата. По выделению
// внутри сообщения (корни несут data-role) у курсора всплывает единственная кнопка «В контекст»:
// фрагмент оседает чипом в зоне над инпутом. Дальше зона либо подмешивается в текст следующего
// сообщения, либо служит контекстом для навыка `/note` (карточка заметки). Popover в проекте нет —
// свой fixed-div через портал.

// selectionText возвращает текст текущего выделения, если оно непусто и лежит ВНУТРИ сообщения
// (assistant/user), иначе null. Позицию меню берём от курсора (mouseup), а не от края выделения —
// так меню всплывает прямо у указателя, а не тянется к правому краю.
function selectionText(): string | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString().trim();
  if (!text) return null;
  const node = sel.getRangeAt(0).commonAncestorContainer;
  const el = node instanceof Element ? node : node.parentElement;
  if (!el?.closest("[data-role='assistant'], [data-role='user']")) return null;
  return text;
}

export function SelectionMenu() {
  const pending = usePendingContext();
  const [menu, setMenu] = useState<{ text: string; x: number; y: number } | null>(
    null,
  );

  useEffect(() => {
    // mouseup — выделение уже установлено (в следующем тике); меню ставим у координат курсора.
    const onMouseUp = (e: MouseEvent) => {
      const x = e.clientX;
      const y = e.clientY;
      window.setTimeout(() => {
        const text = selectionText();
        setMenu(text ? { text, x, y } : null);
      }, 0);
    };
    // selectionchange гасит меню при снятии выделения (клик мимо); scroll — прячем.
    const onSelectionChange = () => {
      if (window.getSelection()?.isCollapsed) setMenu(null);
    };
    const onScroll = () => setMenu(null);
    document.addEventListener("mouseup", onMouseUp);
    document.addEventListener("selectionchange", onSelectionChange);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mouseup", onMouseUp);
      document.removeEventListener("selectionchange", onSelectionChange);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, []);

  if (!menu || !pending) return null;

  const add = () => {
    pending.add({ kind: "quote", text: menu.text });
    window.getSelection()?.removeAllRanges();
    setMenu(null);
  };

  return createPortal(
    <div
      style={{
        position: "fixed",
        // У курсора: чуть ниже-правее точки отпускания мыши, с зажимом к краям вьюпорта.
        left: Math.min(menu.x + 6, window.innerWidth - 200),
        top: Math.min(menu.y + 6, window.innerHeight - 44),
        zIndex: 50,
      }}
      // preventDefault на mousedown — клик по кнопке не снимает выделение.
      onMouseDown={(e) => e.preventDefault()}
      className="flex items-center gap-1 rounded-lg border bg-popover p-1 text-popover-foreground shadow-md"
    >
      <button
        type="button"
        onClick={add}
        className="flex cursor-pointer items-center gap-1.5 rounded-md px-2 py-1 text-xs hover:bg-accent hover:text-accent-foreground"
      >
        <Quote className="size-3.5" />
        В контекст
      </button>
    </div>,
    document.body,
  );
}
