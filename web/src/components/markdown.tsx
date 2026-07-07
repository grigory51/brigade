import type { Components } from "react-markdown";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

import { cn } from "@/lib/utils";

// Markdown — статичный рендер готового markdown-тела (заметки памяти и т.п.). Для потоковых
// сообщений ассистента используется MarkdownText поверх assistant-ui runtime; здесь контент
// приходит целиком, поэтому обычный react-markdown. Классы совпадают с markdown-text.tsx,
// чтобы типографика была единой; highlight.js-тема для код-блоков подключена глобально.
export function Markdown({
  children,
  className,
}: {
  children: string;
  className?: string;
}) {
  return (
    <div className={cn("text-sm", className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeHighlight]}
        components={components}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}

const components: Components = {
  h1: ({ className, ...props }) => (
    <h1
      className={cn(
        "mt-5 mb-2 text-xl font-semibold first:mt-0 last:mb-0",
        className,
      )}
      {...props}
    />
  ),
  h2: ({ className, ...props }) => (
    <h2
      className={cn(
        "mt-5 mb-2 text-lg font-semibold first:mt-0 last:mb-0",
        className,
      )}
      {...props}
    />
  ),
  h3: ({ className, ...props }) => (
    <h3
      className={cn(
        "mt-4 mb-1.5 text-base font-semibold first:mt-0 last:mb-0",
        className,
      )}
      {...props}
    />
  ),
  h4: ({ className, ...props }) => (
    <h4
      className={cn(
        "mt-3.5 mb-1 text-base font-medium first:mt-0 last:mb-0",
        className,
      )}
      {...props}
    />
  ),
  p: ({ className, ...props }) => (
    <p
      className={cn("my-3 leading-relaxed first:mt-0 last:mb-0", className)}
      {...props}
    />
  ),
  a: ({ className, ...props }) => (
    <a
      className={cn(
        "text-primary hover:text-primary/80 underline underline-offset-2",
        className,
      )}
      {...props}
    />
  ),
  blockquote: ({ className, ...props }) => (
    <blockquote
      className={cn(
        "border-muted-foreground/30 text-muted-foreground my-3 border-s-2 ps-4",
        className,
      )}
      {...props}
    />
  ),
  ul: ({ className, ...props }) => (
    <ul
      className={cn(
        "marker:text-muted-foreground my-3 ms-5 list-disc [&>li]:mt-1",
        className,
      )}
      {...props}
    />
  ),
  ol: ({ className, ...props }) => (
    <ol
      className={cn(
        "marker:text-muted-foreground my-3 ms-5 list-decimal [&>li]:mt-1",
        className,
      )}
      {...props}
    />
  ),
  li: ({ className, ...props }) => (
    <li className={cn("leading-relaxed", className)} {...props} />
  ),
  hr: ({ className, ...props }) => (
    <hr className={cn("border-muted-foreground/20 my-4", className)} {...props} />
  ),
  strong: ({ className, ...props }) => (
    <strong className={cn("font-semibold", className)} {...props} />
  ),
  table: ({ className, ...props }) => (
    <table
      className={cn(
        "my-3 w-full border-separate border-spacing-0 overflow-y-auto",
        className,
      )}
      {...props}
    />
  ),
  th: ({ className, ...props }) => (
    <th
      className={cn(
        "bg-muted px-3 py-1.5 text-start font-medium first:rounded-ss-lg last:rounded-se-lg",
        className,
      )}
      {...props}
    />
  ),
  td: ({ className, ...props }) => (
    <td
      className={cn(
        "border-muted-foreground/20 border-s border-b px-3 py-1.5 text-start last:border-e",
        className,
      )}
      {...props}
    />
  ),
  code: ({ className, ...props }) => (
    // Инлайн-код и код-блоки: у блочного кода react-markdown проставляет language-* класс,
    // по которому rehype-highlight и наши стили отличают его от инлайна (bg через .hljs/pre).
    <code
      className={cn(
        !String(className ?? "").includes("language-") &&
          "bg-muted rounded-md px-1.5 py-0.5 font-mono text-[0.85em] break-all",
        className,
      )}
      {...props}
    />
  ),
  pre: ({ className, ...props }) => (
    <pre
      className={cn(
        "border-border/50 bg-muted/30 my-3 overflow-x-auto rounded-lg border p-3.5 text-[13px] leading-relaxed",
        className,
      )}
      {...props}
    />
  ),
};
