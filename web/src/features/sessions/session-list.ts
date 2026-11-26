import { type Session } from "@/api/gen/brigade/v1/session_pb";
import { kindLabel } from "@/lib/term/format";

export function sessionListTitle(session: Session): string {
  return session.name || `${session.agentType} · ${kindLabel(session.kind)}`;
}

export function sessionDateLabel(session: Session, now: Date): "Сегодня" | "Вчера" | "Ранее" {
  const timestamp = session.lastMessageAt > 0n ? session.lastMessageAt : session.createdAt;
  const date = new Date(Number(timestamp) * 1000);
  const yesterday = new Date(now);
  // Календарный день может длиться не 24 часа при смене часового пояса или DST.
  yesterday.setDate(yesterday.getDate() - 1);
  if (date.toDateString() === now.toDateString()) return "Сегодня";
  if (date.toDateString() === yesterday.toDateString()) return "Вчера";
  return "Ранее";
}

type SessionListGroup = {
  key: string;
  label: string;
  sessions: Session[];
};

export function isSessionGroupExpanded(group: SessionListGroup, collapsedGroups: ReadonlySet<string>): boolean {
  return !group.label || !collapsedGroups.has(group.key);
}

export function sessionListGroups(sessions: Session[]): SessionListGroup[] {
  const groups = new Map<string, SessionListGroup>();
  const hasNamedGroups = sessions.some((session) => session.groupLabel !== "");
  for (const session of sessions) {
    const key = session.groupLabel || "\0";
    let group = groups.get(key);
    if (!group) {
      group = { key, label: session.groupLabel || (hasNamedGroups ? "Без группы" : ""), sessions: [] };
      groups.set(key, group);
    }
    group.sessions.push(session);
  }
  return [...groups.values()];
}
