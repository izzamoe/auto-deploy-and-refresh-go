import { useId } from "react";
import type { ReactNode } from "react";
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
}

/** White stat card: label, big number, small hint (plan §E). */
export function StatCard({ title, value, hint, icon, children, className, testId }: StatCardProps) {
  const id = useId();
  return (
    <section aria-labelledby={id} data-testid={testId} className={cn("flex flex-col gap-2 rounded-card bg-card p-5 shadow-card", className)}>
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className="text-sm font-medium text-muted-foreground">
          {title}
        </h2>
        {icon && (
          <span aria-hidden="true" className="inline-flex size-9 items-center justify-center rounded-full bg-accent text-accent-foreground [&_svg]:size-4">
            {icon}
          </span>
        )}
      </div>
      <p className="text-3xl font-semibold tracking-tight" data-testid={testId ? `${testId}-value` : undefined}>
        {value}
      </p>
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
      {children}
    </section>
  );
}
