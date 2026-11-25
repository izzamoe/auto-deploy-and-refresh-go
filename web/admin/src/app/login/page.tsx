"use client";

import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { LoginForm } from "@/components/auth/LoginForm";

// Public page (no shell, no session gate; Providers renders it bare). `useSearchParams`
// needs a Suspense boundary in a static export.
function LoginRoute() {
  const params = useSearchParams();
  return <LoginForm next={params.get("next")} />;
}

export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginRoute />
    </Suspense>
  );
}
