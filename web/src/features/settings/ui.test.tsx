// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import { KeyRound, MessageSquareText } from "lucide-react";
import { afterEach, expect, test } from "vitest";
import { SectionHeader, SettingsGroup } from "./ui";

afterEach(cleanup);

test("settings groups have distinct accessible names below the page heading", () => {
  const view = render(<>
    <SectionHeader title="Общее" />
    <SettingsGroup title="Чат" icon={MessageSquareText}><button>Отправка сообщений</button></SettingsGroup>
    <SettingsGroup title="SSH-ключ агента" icon={KeyRound} description="Доступ к репозиториям"><button>Скопировать</button></SettingsGroup>
  </>);
  expect(view.getByRole("heading", { name: "Общее", level: 2 })).toBeTruthy();
  expect(view.getByRole("heading", { name: "Чат", level: 3 })).toBeTruthy();
  expect(view.getByRole("region", { name: "Чат" }).contains(view.getByText("Отправка сообщений"))).toBe(true);
  expect(view.getByRole("region", { name: "SSH-ключ агента" }).contains(view.getByText("Скопировать"))).toBe(true);
});
