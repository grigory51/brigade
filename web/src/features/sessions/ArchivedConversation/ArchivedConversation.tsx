import { Virtuoso } from "react-virtuoso";
import type { AcpMessage } from "@/api/gen/brigade/v1/acp_pb";
import { CollapsibleLinkText } from "@/components/assistant-ui/UserMessageText/UserMessageText";
import { cn } from "@/lib/utils";

const dateFormat = new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "short" });
const deliveryLabel: Record<string, string> = {
  draft: "Черновик", stale: "Устаревший черновик", failed: "Не отправлено",
  uncertain: "Доставка не подтверждена", sending: "Отправка не подтверждена",
};

export function ArchivedConversation({ messages }: { messages: AcpMessage[] }) {
  return (
    <Virtuoso
      role="list"
      aria-label="Архив переписки"
      className="h-full min-w-0"
      data={messages}
      computeItemKey={(_, message) => message.id}
      initialTopMostItemIndex={{ index: "LAST", align: "end" }}
      itemContent={(index, message) => {
        const note = message.source === "brigade:contact-note" || (message.source === "brigade" && message.author === "owner" && message.delivery === "received");
        const label = note ? "Личная заметка" : deliveryLabel[message.delivery];
        return (
          <div role="listitem" aria-posinset={index + 1} aria-setsize={messages.length}
            className="mx-auto flex w-full max-w-3xl flex-col px-4 py-1">
            <div className={cn("min-w-0 max-w-[85%] rounded-xl border px-3 py-2 text-sm",
              note ? "self-center border-dashed" : message.author === "contact" ? "self-start rounded-bl-sm bg-card" : "self-end rounded-br-sm bg-accent")}>
              {label && <div className="mb-1 text-xs text-muted-foreground">{label}</div>}
              {message.delivery === "deleted" ? "Сообщение удалено" : <CollapsibleLinkText text={message.content} />}
              {message.createdAt > 0n && <div className="mt-1 text-right text-xs text-muted-foreground">{dateFormat.format(Number(message.createdAt) * 1000)}</div>}
            </div>
          </div>
        );
      }}
    />
  );
}
