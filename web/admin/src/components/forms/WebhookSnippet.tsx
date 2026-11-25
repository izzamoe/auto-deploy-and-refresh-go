"use client";

import "@/i18n/id/form";
import { CopyableSnippet } from "@/components/common/CopyableSnippet";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

function curlSnippet(origin: string, secret: string): string {
  return `curl -s -w '\\n%{http_code}' -X POST ${origin}/webhook -H "Authorization: Bearer ${secret}" -H "Content-Type: application/json" -d '{"tag":"v1.0.0"}'`;
}

function actionsSnippet(origin: string): string {
  return [
    "name: Deploy Webhook",
    "on:",
    "  release:",
    "    types: [published]",
    "jobs:",
    "  notify:",
    "    runs-on: ubuntu-latest",
    "    steps:",
    "      - name: Trigger deploy webhook",
    "        run: |",
    `          curl -s -w '\\n%{http_code}' -X POST ${origin}/webhook \\`,
    '            -H "Authorization: Bearer ${{ secrets.DEPLOY_WEBHOOK_SECRET }}" \\',
    '            -H "Content-Type: application/json" \\',
    '            -d \'{"tag":"${{ github.event.release.tag_name }}"}\'',
  ].join("\n");
}

/** Shown ONCE after creating an app: the secret returned by the server (FR-076) + snippets. */
export function WebhookSnippet({ appName, secret, onDismiss }: { appName: string; secret: string; onDismiss: () => void }) {
  const { t } = useI18n();
  const origin = window.location.origin;
  return (
    <section data-testid="webhook-snippet" aria-labelledby="secret-title" className="space-y-5 rounded-card bg-card p-6 shadow-card">
      <div>
        <h2 id="secret-title" className="text-xl font-semibold">
          {t("form.secret.title", { name: appName })}
        </h2>
        <p className="text-sm text-muted-foreground">{t("form.secret.desc")}</p>
      </div>
      <p role="alert" className="rounded-xl bg-warning-soft px-4 py-3 text-sm text-warning-soft-foreground">
        {t("form.secret.once")}
      </p>
      <CopyableSnippet label={t("form.secret.value")} code={secret} testId="webhook-secret-value" />
      <CopyableSnippet label={t("form.snippet.curl")} code={curlSnippet(origin, secret)} testId="webhook-curl-snippet" />
      <CopyableSnippet label={t("form.snippet.gha")} code={actionsSnippet(origin)} testId="webhook-gha-snippet" />
      <div className="flex justify-end">
        <Button type="button" onClick={onDismiss}>
          {t("form.secret.done")}
        </Button>
      </div>
    </section>
  );
}

/** Non-dismissable reference on the edit form (the stored secret is hashed: placeholder). */
export function WebhookReference({ appName, secret }: { appName: string; secret: string }) {
  const { t } = useI18n();
  const origin = window.location.origin;
  const effective = secret.trim() === "" ? t("form.snippet.placeholder") : secret;
  return (
    <section data-testid="webhook-reference" aria-labelledby="ref-title" className="space-y-5 rounded-card bg-card p-6 shadow-card">
      <div>
        <h2 id="ref-title" className="text-xl font-semibold">
          {t("form.snippet.refTitle", { name: appName })}
        </h2>
        <p className="text-sm text-muted-foreground">{t("form.snippet.refDesc")}</p>
      </div>
      <CopyableSnippet label={t("form.snippet.trigger")} code={curlSnippet(origin, effective)} testId="webhook-ref-curl-snippet" />
      <CopyableSnippet label={t("form.snippet.gha")} code={actionsSnippet(origin)} testId="webhook-ref-gha-snippet" />
    </section>
  );
}
