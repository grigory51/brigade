import { useParams } from "react-router-dom";
import { SessionGuard } from "@/lib/SessionGuard";

// SessionPage — экран одной сессии по маршруту /s/:sessionId. Режим (CLI или ACP)
// определяется не URL, а данными сессии: выбор делает SessionGuard по её kind.
export function SessionPage() {
  const { sessionId } = useParams<{ sessionId: string }>();
  return <SessionGuard sessionId={sessionId} />;
}
