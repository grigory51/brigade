import { useMessagePartText } from "@assistant-ui/react";
import { useLayoutEffect, useRef, useState, type FC, type ReactNode } from "react";
import { LinkWithPreview } from "@/components/assistant-ui/link-with-preview";
import { cn } from "@/lib/utils";

const URL_PATTERN = /https?:\/\/[^\s<]+/gi;
const TRAILING_URL_PUNCTUATION = /[.,!?;:)}\]]+$/;

export const UserMessageText: FC = () => {
  const { text: rawText, status } = useMessagePartText();
  // Preserve indentation and paragraph breaks, but do not render an empty tail.
  const text = rawText.trimEnd();
  const contentRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useLayoutEffect(() => {
    const element = contentRef.current;
    if (!element || expanded) return;
    const update = () => setOverflowing(element.scrollHeight > element.clientHeight);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, [expanded, text]);

  const parts: ReactNode[] = [];
  let offset = 0;
  for (const match of text.matchAll(URL_PATTERN)) {
    const index = match.index;
    const rawURL = match[0];
    const url = rawURL.replace(TRAILING_URL_PUNCTUATION, "");
    parts.push(text.slice(offset, index));
    parts.push(
      <LinkWithPreview key={`${index}-${url}`} href={url}>
        {url}
      </LinkWithPreview>,
    );
    parts.push(rawURL.slice(url.length));
    offset = index + rawURL.length;
  }
  parts.push(text.slice(offset));

  return (
    <div data-status={status.type}>
      <div
        ref={contentRef}
        className={cn(
          "whitespace-pre-wrap break-words",
          !expanded && "max-h-40 overflow-hidden",
        )}
      >
        {parts}
      </div>
      {overflowing && (
        <button
          type="button"
          className="text-primary mt-2 text-xs font-medium hover:underline"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? "Свернуть" : "Развернуть"}
        </button>
      )}
    </div>
  );
};
