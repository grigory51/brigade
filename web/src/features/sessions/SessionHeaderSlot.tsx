import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";

// Содержимое под-хедера активной сессии: title (например, идентификатор) и right
// (контекстные действия — индикатор соединения CLI, UsageMeter ACP). Контент сессии
// публикует эти элементы через useSessionHeader, а оболочка SessionLayout рендерит их
// в шапке SidebarInset. Это разрывает зависимость экранов сессии от внешней оболочки:
// CliSession/AcpSession возвращают только свой контент, не оборачивая его в Layout.
type SessionHeaderValue = {
  title: ReactNode;
  right: ReactNode;
  setHeader: (header: { title?: ReactNode; right?: ReactNode }) => void;
};

const SessionHeaderContext = createContext<SessionHeaderValue | null>(null);

export function SessionHeaderProvider({ children }: { children: ReactNode }) {
  const [header, setHeader] = useState<{
    title: ReactNode;
    right: ReactNode;
  }>({ title: null, right: null });

  return (
    <SessionHeaderContext.Provider
      value={{
        title: header.title,
        right: header.right,
        setHeader: (next) =>
          setHeader({ title: next.title ?? null, right: next.right ?? null }),
      }}
    >
      {children}
    </SessionHeaderContext.Provider>
  );
}

// useSessionHeaderSlot читает текущее содержимое под-хедера (для оболочки).
export function useSessionHeaderSlot() {
  const ctx = useContext(SessionHeaderContext);
  if (!ctx) {
    throw new Error("useSessionHeaderSlot must be used within SessionHeaderProvider");
  }
  return { title: ctx.title, right: ctx.right };
}

/**
 * useSessionHeader публикует title/right активной сессии в под-хедер оболочки на
 * время монтирования компонента и очищает их при размонтировании (смена сессии или
 * переход на пустое состояние). Контент сессии вызывает этот хук вместо обёртки Layout.
 */
export function useSessionHeader(header: {
  title?: ReactNode;
  right?: ReactNode;
}) {
  const ctx = useContext(SessionHeaderContext);
  if (!ctx) {
    throw new Error("useSessionHeader must be used within SessionHeaderProvider");
  }
  const { setHeader } = ctx;
  const { title, right } = header;

  useEffect(() => {
    setHeader({ title, right });
    return () => setHeader({ title: null, right: null });
  }, [setHeader, title, right]);
}
