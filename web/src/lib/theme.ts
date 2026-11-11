import { useEffect, useState } from "react";

export type Theme = "system" | "light" | "dark";
const KEY = "hm-theme";

function read(): Theme {
  try {
    const t = localStorage.getItem(KEY);
    if (t === "light" || t === "dark") return t;
  } catch {
    /* 隐私模式下可能读不到 */
  }
  return "system";
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(read);
  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") delete root.dataset.theme;
    else root.dataset.theme = theme;
    try {
      if (theme === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, theme);
    } catch {
      /* 存不了就只在本次访问生效 */
    }
  }, [theme]);
  return [theme, setTheme];
}

export const THEME_LABEL: Record<Theme, string> = { system: "跟随系统", light: "浅色", dark: "深色" };
export const nextTheme = (t: Theme): Theme => (t === "system" ? "light" : t === "light" ? "dark" : "system");
