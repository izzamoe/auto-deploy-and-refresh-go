import { useEffect, useState } from "react";
import { useTheme } from "next-themes";
import { Moon, Sun } from "lucide-react";

import { Button } from "@/components/ui/button";

export function ThemeToggle() {
	const { resolvedTheme, setTheme } = useTheme();
	const [mounted, setMounted] = useState(false);

	// next-themes only knows the resolved theme after mount; render a stable
	// placeholder until then to avoid a hydration/icon flash.
	useEffect(() => setMounted(true), []);

	const isDark = resolvedTheme === "dark";

	return (
		<Button
			type="button"
			variant="ghost"
			size="icon"
			aria-label="Toggle theme"
			onClick={() => setTheme(isDark ? "light" : "dark")}
		>
			{mounted && isDark ? <Sun className="size-5" /> : <Moon className="size-5" />}
		</Button>
	);
}
