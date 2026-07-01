import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// Объединяет условные классы и разрешает конфликты Tailwind-утилит.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
