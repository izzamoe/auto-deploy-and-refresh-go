import type { Metadata } from "next";
import { Providers } from "@/components/shell/Providers";
import "../styles/globals.css";

export const metadata: Metadata = { title: "auto-deploy" };

// Server component: the statically prerendered shell. The default language is `id`
// (Q-9); the client i18n provider updates <html lang> after hydration. `suppressHydrationWarning`
// is for next-themes, which sets the `dark` class on <html> before React hydrates.
export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="id" suppressHydrationWarning>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
