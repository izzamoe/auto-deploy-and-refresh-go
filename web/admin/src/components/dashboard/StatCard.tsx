import { useId } from "react";
import type { ReactNode } from "react";
import { CARD_TITLE } from "@/components/common/cardStyles";
import { cn } from "@/lib/utils";

interface StatCardProps {
  title: string;
  value: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  /** Decorative extra (e.g. mini bars). */
  children?: ReactNode;
  className?: string;
  testId?: string;
  /** Colour of the big number: green for positive figures, red for failures. */
  tone?: "neutral" | "success" | "danger";
}

/** White stat card: label, big number, small hint (plan §E). */
export function StatCard({ title, value, hint, icon, children, className, testId, tone = "neutral" }: StatCardProps) {
  const id = useId();
  return (
    <section aria-labelledby={id} data-testid={testId} className={cn("flex flex-col gap-2 rounded-card bg-card p-5 shadow-card", className)}>
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className={CARD_TITLE}>
          {title}
        </h2>
        {icon && (
          <span aria-hidden="true" className={cn("inline-flex size-9 items-center justify-center rounded-full [&_svg]:size-4", tone === "danger" ? "bg-destructive text-destructive-foreground" : "bg-primary text-primary-foreground")}>
            {icon}
          </span>
        )}
      </div>
      <p className={cn("text-3xl font-semibold tracking-tight", tone === "success" && "text-primary-text", tone === "danger" && "text-danger-chart")} data-testid={testId ? `${testId}-value` : undefined}>
        {value}
      </p>
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
      {children}
    </section>
  );
}
