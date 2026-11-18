import { useEffect, useState } from "react";
import { Loader2, RefreshCw } from "lucide-react";
import { telegramClient } from "@/api/client";
import { Button } from "@/components/ui/button";

type Props = { sessionId: string; messageId: string };

// BusinessPhoto не получает file_id: backend проверяет владельца и состояние сообщения
// перед временной загрузкой фото из Telegram.
export function BusinessPhoto({ sessionId, messageId }: Props) {
  const [url, setUrl] = useState("");
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let active = true;
    let objectURL = "";
    setUrl("");
    setError(false);
    void telegramClient.getBusinessPhoto({ sessionId, messageId }).then((photo) => {
      objectURL = URL.createObjectURL(new Blob([new Uint8Array(photo.data)], { type: photo.contentType || "image/jpeg" }));
      if (active) setUrl(objectURL);
      else URL.revokeObjectURL(objectURL);
    }).catch(() => {
      if (active) setError(true);
    });
    return () => {
      active = false;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [sessionId, messageId, attempt]);

  if (error) {
    return <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground" role="alert">
      Не удалось загрузить фото
      <Button type="button" size="sm" variant="ghost" className="h-7 px-2" onClick={() => setAttempt((value) => value + 1)}><RefreshCw className="size-3.5" />Повторить</Button>
    </div>;
  }
  if (!url) {
    return <div className="mt-2 flex h-40 items-center justify-center rounded-lg bg-muted/50 text-xs text-muted-foreground" role="status"><Loader2 className="mr-2 size-3.5 animate-spin" />Загружаем фото…</div>;
  }
  return <img src={url} alt="Фото из Telegram" className="mt-2 max-h-96 max-w-full rounded-lg object-contain" />;
}
