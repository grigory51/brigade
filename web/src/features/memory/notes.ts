// Метаданные типов заметок — акцентные цвета из дизайн-хендоффа (в палитре brigade их пока
// нет; подобраны в гармонии с терракотовым брендом). Каждый тип = точка/лейбл цветом.
export const NOTE_TYPES = [
  { value: "idea", label: "Идея", color: "#c96442" },
  { value: "decision", label: "Решение", color: "#d9a441" },
  { value: "insight", label: "Инсайт", color: "#6fa564" },
  { value: "todo", label: "Задача", color: "#7c9fd6" },
  { value: "question", label: "Вопрос", color: "#b98cd1" },
  { value: "reference", label: "Справка", color: "#a8a49a" },
] as const;

// TOPIC_COLORS — палитра акцентов тем (тот же набор из шести цветов).
export const TOPIC_COLORS = [
  "#c96442",
  "#d9a441",
  "#6fa564",
  "#7c9fd6",
  "#b98cd1",
  "#a8a49a",
];

// noteType возвращает мету типа (лейбл + цвет), с откатом на первый тип для неизвестных.
export function noteType(value: string) {
  return NOTE_TYPES.find((t) => t.value === value) ?? NOTE_TYPES[0];
}

// softColor — полупрозрачный фон акцента (бейджи/чипы/аватары), 16% от hex-цвета.
export function softColor(hex: string): string {
  return hexToRgba(hex, 0.16);
}

function hexToRgba(hex: string, alpha: number): string {
  const h = hex.replace("#", "");
  const r = parseInt(h.slice(0, 2), 16);
  const g = parseInt(h.slice(2, 4), 16);
  const b = parseInt(h.slice(4, 6), 16);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

// plural — русская форма по числу: plural(n, ["заметка","заметки","заметок"]).
export function plural(n: number, forms: [string, string, string]): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return forms[0];
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) return forms[1];
  return forms[2];
}

// noteCountLabel — «N заметок» с корректной формой.
export function noteCountLabel(n: number): string {
  return `${n} ${plural(n, ["заметка", "заметки", "заметок"])}`;
}
