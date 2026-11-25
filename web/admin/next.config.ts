import type { NextConfig } from "next";

// Static export served by the Go binary under /admin (spike-report.md, main variant).
// Output goes to web/admin/out and is copied to internal/admin/web/admin/dist by
// scripts/embed-admin-ui.sh. D3-a: TypeScript 7 type-checks inside `next build`.
const config: NextConfig = {
  output: "export",
  basePath: "/admin",
  trailingSlash: true,
  images: { unoptimized: true },
  reactStrictMode: true,
  typescript: { ignoreBuildErrors: false },
};

export default config;
