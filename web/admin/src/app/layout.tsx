import type { Metadata } from "next";
import "../styles/globals.css";

export const metadata: Metadata = { title: "auto-deploy" };

// Server component: statically prerendered shell. The default language is `id`
// (Q-9); the client i18n provider (T083) updates <html lang> after hydration.
export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="id">
      <body>{children}</body>
    </html>
  );
}
