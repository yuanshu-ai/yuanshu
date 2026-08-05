import { useI18n } from "../i18n";
import { useTheme } from "./ThemeProvider";
import type { ThemeMode } from "./theme";

export function ThemeToggle() {
  const { locale, t } = useI18n();
  const { mode, setMode } = useTheme();
  const options: Array<{ value: ThemeMode; label: string }> = [
    { value: "system", label: t("theme.system") },
    { value: "light", label: t("theme.light") },
    { value: "dark", label: t("theme.dark") },
  ];
  return <label className="theme-control">
    <span className="sr-only">{t("theme.label")}</span>
    <select aria-label={t("theme.label")} value={mode} onChange={(event) => setMode(event.target.value as ThemeMode)} lang={locale}>
      {options.map((option) => <option value={option.value} key={option.value}>{option.label}</option>)}
    </select>
  </label>;
}
