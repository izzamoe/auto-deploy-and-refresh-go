"use client";

import { Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import { RailTip } from "./RailTip";

// next-themes only knows the resolved theme after mount; a stable placeholder icon is
// rendered until then, so the prerendered HTML and the first client render agree.
export function ThemeToggle({ className }: { className?: string }) {
  const { t } = useI18n();
  const { resolvedTheme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const dark = mounted && resolvedTheme === "dark";
  return (
    <RailTip label={dark ? t("shell.theme.light") : t("shell.theme.dark")}>
      <button
        type="button"
        onClick={() => setTheme(dark ? "light" : "dark")}
        aria-label={t("shell.theme.toggle")}
        className={cn(
          "inline-flex size-11 items-center justify-center rounded-full text-rail-muted hover:bg-white/10 hover:text-rail-foreground focus-visible:outline-rail-foreground",
          className,
        )}
      >
        {dark ? <Sun className="size-5" aria-hidden="true" /> : <Moon className="size-5" aria-hidden="true" />}
      </button>
    </RailTip>
  );
}
