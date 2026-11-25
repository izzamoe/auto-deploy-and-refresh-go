"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

// Becomes 404.html in the export; the Go handler serves it with status 404 (D1).
// Centered in the available area (grid + place-items), balanced wrapping so the second line
// is never a single orphan word, and the way home is a real button, not a text link.
export default function NotFound() {
  const { t } = useI18n();
  return (
    <div data-testid="not-found-wrap" className="grid min-h-[70dvh] place-items-center p-4">
      <main data-testid="not-found" className="w-full max-w-md rounded-card bg-card p-8 text-center shadow-card">
        <p aria-hidden="true" className="text-5xl font-semibold text-primary-text">
          404
        </p>
        <h1 className="mt-2 text-balance text-2xl font-semibold">{t("notfound.title")}</h1>
        <p className="mx-auto mt-2 max-w-[32ch] text-balance text-muted-foreground">{t("notfound.body")}</p>
        <Button asChild size="lg" className="mt-6">
          <Link href="/">{t("notfound.home")}</Link>
        </Button>
      </main>
    </div>
  );
}
