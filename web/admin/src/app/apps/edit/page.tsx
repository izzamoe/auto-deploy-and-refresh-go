"use client";

import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { AppForm } from "@/components/forms/AppForm";
import { useI18n } from "@/i18n";

// The id comes from ?id= (static export: no dynamic segment), read inside <Suspense>.
function EditRoute() {
  const { t } = useI18n();
  const id = useSearchParams().get("id");
  if (!id) {
    return (
      <p role="alert" className="rounded-card bg-card p-6 shadow-card">
        {t("form.notFound")}
      </p>
    );
  }
  return <AppForm appId={id} />;
}

export default function EditAppPage() {
  return (
    <Suspense fallback={null}>
      <EditRoute />
    </Suspense>
  );
}
