import { describe, expect, test } from "vitest";
import { Session, SessionKind } from "@/api/gen/brigade/v1/session_pb";
import { isSessionGroupExpanded, sessionListTitle, sessionDateLabel, sessionListGroups } from "./session-list";

const timestamp = (date: Date): bigint => BigInt(Math.floor(date.getTime() / 1000));

describe("sessionDateLabel", () => {
  test("uses local calendar days, not elapsed hours", () => {
    const now = new Date(2026, 10, 25, 0, 5);
    expect(sessionDateLabel(new Session({ lastMessageAt: timestamp(new Date(2026, 10, 25, 0, 1)) }), now)).toBe("Сегодня");
    expect(sessionDateLabel(new Session({ lastMessageAt: timestamp(new Date(2026, 10, 24, 23, 59)) }), now)).toBe("Вчера");
    expect(sessionDateLabel(new Session({ lastMessageAt: timestamp(new Date(2026, 10, 23, 23, 59)) }), now)).toBe("Ранее");
  });

  test.each([
    [new Date(2027, 0, 1, 12), new Date(2026, 11, 31, 12)],
    [new Date(2028, 2, 1, 12), new Date(2028, 1, 29, 12)],
    [new Date(2026, 2, 9, 0, 5), new Date(2026, 2, 8, 0, 1)],
    [new Date(2026, 10, 2, 0, 5), new Date(2026, 10, 1, 0, 1)],
  ])("handles calendar boundaries at %s", (now, previous) => {
    expect(sessionDateLabel(new Session({ lastMessageAt: timestamp(previous) }), now)).toBe("Вчера");
  });

  test("prefers the last message date and falls back to creation for empty sessions", () => {
    const now = new Date(2026, 10, 25, 12);
    const session = new Session({
      createdAt: timestamp(new Date(2026, 10, 24, 12)),
      lastMessageAt: timestamp(now),
    });
    expect(sessionDateLabel(session, now)).toBe("Сегодня");
    session.lastMessageAt = 0n;
    expect(sessionDateLabel(session, now)).toBe("Вчера");
    expect(sessionDateLabel(new Session(), now)).toBe("Ранее");
  });
});

test("uses the same title and fallback in sidebar and palette", () => {
  expect(sessionListTitle(new Session({ name: "Задача", agentType: "claude" }))).toBe("Задача");
  expect(sessionListTitle(new Session({ agentType: "claude", kind: SessionKind.ACP }))).toBe("claude · ACP");
});

describe("sessionListGroups", () => {
  const sessions = [
    new Session({ id: "1", name: "Первая", groupLabel: "Работа" }),
    new Session({ id: "2", name: "Вторая", groupLabel: "Личное" }),
    new Session({ id: "3", name: "Третья", groupLabel: "Работа", lastMessage: "Важный ответ" }),
    new Session({ id: "4", name: "Без имени группы" }),
  ];

  test("preserves equal named groups and the existing order within each group", () => {
    const groups = sessionListGroups(sessions);
    expect(groups.map(({ key, label }) => [key, label])).toEqual([
      ["Работа", "Работа"], ["Личное", "Личное"], ["\0", "Без группы"],
    ]);
    expect(groups[0].sessions).toEqual([sessions[0], sessions[2]]);
    expect(sessions.map(({ id }) => id)).toEqual(["1", "2", "3", "4"]);
  });

  test("keeps ungrouped sessions together for date headings without a named header", () => {
    expect(sessionListGroups([sessions[3]])).toEqual([
      { key: "\0", label: "", sessions: [sessions[3]] },
    ]);
    expect(sessionListGroups([])).toEqual([]);
  });

  test("respects persisted collapse independently of palette search", () => {
    const collapsed = new Set(["Работа", "\0"]);
    const groups = sessionListGroups(sessions);
    expect(isSessionGroupExpanded(groups[0], collapsed)).toBe(false);
    expect(isSessionGroupExpanded(groups[1], collapsed)).toBe(true);
    expect(isSessionGroupExpanded(groups[2], collapsed)).toBe(false);
    expect([...collapsed]).toEqual(["Работа", "\0"]);
    expect(isSessionGroupExpanded(sessionListGroups([sessions[3]])[0], collapsed)).toBe(true);
  });
});
