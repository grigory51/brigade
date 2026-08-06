import { useState, type ComponentProps } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
import { HoverCard } from "radix-ui";

import { linkPreviewClient } from "@/api/client";
import { cn } from "@/lib/utils";

type Preview = {
  url: string;
  title: string;
  description: string;
  imageUrl: string;
  siteName: string;
  iconUrl: string;
};

export function LinkWithPreview({ href, className, children, ...props }: ComponentProps<"a">) {
  const [preview, setPreview] = useState<Preview | null>(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);

  const previewable = !!href && /^https?:\/\//i.test(href);
  const load = () => {
    if (!href || preview || loading || failed || !previewable) return;
    setLoading(true);
    void linkPreviewClient.get({ url: href }).then((result) => {
      setPreview(result);
    }).catch(() => {
      setFailed(true);
    }).finally(() => {
      setLoading(false);
    });
  };

  const anchor = (
    <a
      {...props}
      href={href}
      target={previewable ? "_blank" : undefined}
      rel={previewable ? "noreferrer" : undefined}
      className={cn("text-primary hover:text-primary/80 cursor-pointer underline underline-offset-2", className)}
    >
      {children}
    </a>
  );

  if (!previewable) return anchor;

  return (
    <HoverCard.Root openDelay={300} closeDelay={100} onOpenChange={(open) => open && load()}>
      <HoverCard.Trigger asChild>{anchor}</HoverCard.Trigger>
      <HoverCard.Portal>
        <HoverCard.Content
          sideOffset={8}
          className="border-border bg-popover text-popover-foreground z-50 w-80 overflow-hidden rounded-xl border shadow-xl outline-none"
        >
          {loading && (
            <div className="text-muted-foreground flex h-24 items-center justify-center gap-2 text-xs">
              <Loader2 className="size-4 animate-spin" />
              Загружаю ссылку…
            </div>
          )}
          {failed && (
            <div className="text-muted-foreground flex h-20 items-center justify-center text-xs">
              Предпросмотр недоступен
            </div>
          )}
          {preview && (
            <div>
              {preview.imageUrl && (
                <img src={preview.imageUrl} alt="" className="h-32 w-full object-cover" />
              )}
              <div className="space-y-1.5 p-3">
                <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[11px]">
                  {preview.iconUrl && <img src={preview.iconUrl} alt="" className="size-3.5 rounded-sm" />}
                  <span className="truncate">{preview.siteName}</span>
                  <ExternalLink className="ml-auto size-3 shrink-0" />
                </div>
                <div className="line-clamp-2 text-sm leading-snug font-medium">{preview.title}</div>
                {preview.description && (
                  <div className="text-muted-foreground line-clamp-3 text-xs leading-relaxed">{preview.description}</div>
                )}
              </div>
            </div>
          )}
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  );
}
