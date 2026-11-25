"use client";

// App create/edit form (FR-074/075/076, FR-028, FR-040, B53, B54, B55).
//
//  - CREATE: ONE atomic POST /apps carrying env + args (B55): either the app exists with its
//    env/args or nothing was created, so there is no half-saved state. The secret the server
//    returns is shown once (FR-076).
//  - EDIT: env and args are loaded separately. A part that is not loaded (or failed to load)
//    is disabled and NEVER sent (B53); a part whose text did not change is NOT sent either
//    (FR-074); the form state is local, so refetches and notifications never reset typing (B54).
import "@/i18n/id/form";
import "@/i18n/id/unit";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import dynamic from "next/dynamic";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Notice, useNotice } from "@/components/common/Notice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { ApiError, api, describeError } from "@/lib/api/client";
import type { Schemas } from "@/lib/api/client";
import { envVarsToText, argsToText, firstInvalidEnvName, normalizeArgsText, normalizeEnvText, parseArgsText, parseEnvText } from "@/lib/envargs";
import { queryKeys } from "@/lib/query";
import { ArgsEditor } from "./ArgsEditor";
import { EnvEditor } from "./EnvEditor";
import { WebhookReference, WebhookSnippet } from "./WebhookSnippet";

const ServiceUnitDialog = dynamic(() => import("@/components/apps/ServiceUnitDialog").then((m) => m.ServiceUnitDialog), { ssr: false });

type App = Schemas["App"];

const KNOWN_FIELDS = ["name", "binaryPath", "serviceName", "githubRepo", "artifactName", "webhookSecret", "runAsUser", "healthUrl", "envVars", "args"] as const;
type FieldKey = (typeof KNOWN_FIELDS)[number];
type FieldErrors = Partial<Record<FieldKey, string>>;

const REPO_RE = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

interface Values {
  name: string;
  serviceName: string;
  binaryPath: string;
  githubRepo: string;
  artifactName: string;
  webhookSecret: string;
  enabled: boolean;
  runAsUser: string;
  hardening: boolean;
  healthUrl: string;
  requireDigest: boolean;
}

const EMPTY: Values = { name: "", serviceName: "", binaryPath: "", githubRepo: "", artifactName: "", webhookSecret: "", enabled: true, runAsUser: "", hardening: false, healthUrl: "", requireDigest: false };

function fromApp(app: App): Values {
  return {
    name: app.name,
    serviceName: app.serviceName,
    binaryPath: app.binaryPath,
    githubRepo: app.githubRepo,
    artifactName: app.artifactName,
    webhookSecret: "",
    enabled: app.enabled,
    runAsUser: app.runAsUser ?? "",
    hardening: app.hardening ?? false,
    healthUrl: app.healthUrl ?? "",
    requireDigest: app.requireDigest ?? false,
  };
}

function Field({ id, label, error, hint, children }: { id: string; label: string; error?: string; hint?: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-danger-soft-foreground">
          {error}
        </p>
      )}
      {hint && (
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {hint}
        </p>
      )}
    </div>
  );
}

function describedBy(id: string, error?: string, hint?: string): string | undefined {
  const ids = [error ? `${id}-error` : "", hint ? `${id}-hint` : ""].filter(Boolean);
  return ids.length ? ids.join(" ") : undefined;
}

function SwitchRow({ id, label, hint, checked, onChange }: { id: string; label: string; hint: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-2xl bg-secondary p-4">
      <div className="space-y-0.5">
        <Label htmlFor={id}>{label}</Label>
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {hint}
        </p>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} aria-describedby={`${id}-hint`} />
    </div>
  );
}

interface InnerProps {
  appId?: string;
  initial: App | null;
}

function AppFormInner({ appId, initial }: InnerProps) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const router = useRouter();
  const editing = appId !== undefined;
  const { notice, success, error: failNotice, clear } = useNotice();

  const [v, setV] = useState<Values>(initial ? fromApp(initial) : EMPTY);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState("");
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<{ name: string; secret: string } | null>(null);
  const [unitOpen, setUnitOpen] = useState(false);

  // env / args: separate resources, loaded only when editing.
  const envQ = useQuery({
    queryKey: ["app-env", appId],
    queryFn: ({ signal }) => api.getAppEnv(appId as string, { signal }),
    enabled: editing,
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  const argsQ = useQuery({
    queryKey: ["app-args", appId],
    queryFn: ({ signal }) => api.getAppArgs(appId as string, { signal }),
    enabled: editing,
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  const [envText, setEnvText] = useState("");
  const [envBase, setEnvBase] = useState<string | null>(editing ? null : "");
  const [argsText, setArgsText] = useState("");
  const [argsBase, setArgsBase] = useState<string | null>(editing ? null : "");

  // Take the loaded values ONCE; later refetches/notifications never overwrite what is typed (B54).
  useEffect(() => {
    if (envQ.data && envBase === null) {
      const text = envVarsToText(envQ.data.envVars);
      setEnvText(text);
      setEnvBase(normalizeEnvText(text));
    }
  }, [envQ.data, envBase]);
  useEffect(() => {
    if (argsQ.data && argsBase === null) {
      const text = argsToText(argsQ.data.args);
      setArgsText(text);
      setArgsBase(normalizeArgsText(text));
    }
  }, [argsQ.data, argsBase]);

  const envStatus = envBase !== null ? "ready" : envQ.isError ? "error" : "loading";
  const argsStatus = argsBase !== null ? "ready" : argsQ.isError ? "error" : "loading";
  const envDirty = editing && envBase !== null && normalizeEnvText(envText) !== envBase;
  const argsDirty = editing && argsBase !== null && normalizeArgsText(argsText) !== argsBase;

  const set = <K extends keyof Values>(key: K, value: Values[K]) => setV((prev) => ({ ...prev, [key]: value }));

  function applyServerError(err: unknown): void {
    if (err instanceof ApiError && err.fieldErrors.length > 0) {
      const next: FieldErrors = {};
      let other = false;
      for (const fe of err.fieldErrors) {
        const key = (fe.field ?? "").split(/[.[]/)[0] as FieldKey;
        if ((KNOWN_FIELDS as readonly string[]).includes(key)) next[key] = t(`form.fieldError.${key}` as MessageKey);
        else other = true;
      }
      setErrors(next);
      setFormError(Object.keys(next).length === 0 || other ? describeError(err, i18n) : "");
      return;
    }
    setErrors({});
    setFormError(describeError(err, i18n));
  }

  function validateClient(): { ok: true; envVars: Schemas["EnvVar"][]; args: string[] } | { ok: false } {
    const next: FieldErrors = {};
    const required = ["name", "serviceName", "binaryPath", "githubRepo", "artifactName"] as const;
    for (const key of required) if (v[key].trim() === "") next[key] = t("form.required");
    if (!next.githubRepo && !REPO_RE.test(v.githubRepo.trim())) next.githubRepo = t("form.fieldError.githubRepo");
    if (v.webhookSecret !== "" && v.webhookSecret.length < 32) next.webhookSecret = t("form.fieldError.webhookSecret");
    const envVars = parseEnvText(envText);
    const bad = firstInvalidEnvName(envVars);
    if (bad !== undefined && (!editing || envDirty)) next.envVars = t("form.env.invalid", { line: bad });
    setErrors(next);
    setFormError("");
    return Object.keys(next).length > 0 ? { ok: false } : { ok: true, envVars, args: parseArgsText(argsText) };
  }

  const fieldsBody = () => ({
    name: v.name.trim(),
    serviceName: v.serviceName.trim(),
    binaryPath: v.binaryPath.trim(),
    githubRepo: v.githubRepo.trim(),
    artifactName: v.artifactName.trim(),
    enabled: v.enabled,
    hardening: v.hardening,
    requireDigest: v.requireDigest,
  });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const checked = validateClient();
    if (!checked.ok) return;
    setBusy(true);
    try {
      if (appId === undefined) {
        const res = await api.createApp({
          ...fieldsBody(),
          ...(v.runAsUser.trim() ? { runAsUser: v.runAsUser.trim() } : {}),
          ...(v.healthUrl.trim() ? { healthUrl: v.healthUrl.trim() } : {}),
          ...(v.webhookSecret ? { webhookSecret: v.webhookSecret } : {}),
          envVars: checked.envVars,
          args: checked.args,
        });
        void client.invalidateQueries({ queryKey: queryKeys.apps });
        void client.invalidateQueries({ queryKey: queryKeys.stats });
        setCreated({ name: res.app.name, secret: res.webhookSecret });
        return;
      }

      // EDIT: the app first; env/args only when loaded AND changed.
      try {
        await api.updateApp(appId, {
          ...fieldsBody(),
          runAsUser: v.runAsUser.trim(),
          healthUrl: v.healthUrl.trim(),
          ...(initial?.isSelf !== undefined ? { isSelf: initial.isSelf } : {}),
          ...(v.webhookSecret ? { webhookSecret: v.webhookSecret } : {}),
        });
      } catch (err) {
        applyServerError(err);
        return;
      }
      void client.invalidateQueries({ queryKey: queryKeys.apps });
      set("webhookSecret", "");
      let partial = false;
      if (envDirty) {
        try {
          await api.saveAppEnv(appId, { envVars: checked.envVars });
          setEnvBase(normalizeEnvText(envText));
        } catch (err) {
          partial = true;
          failNotice(t("form.partial.env", { reason: describeError(err, i18n) }));
        }
      }
      if (argsDirty && !partial) {
        try {
          await api.saveAppArgs(appId, { args: checked.args });
          setArgsBase(normalizeArgsText(argsText));
        } catch (err) {
          partial = true;
          failNotice(t("form.partial.args", { reason: describeError(err, i18n) }));
        }
      }
      if (!partial) success(t("form.updated"));
      setErrors({});
      setFormError("");
    } catch (err) {
      applyServerError(err);
    } finally {
      setBusy(false);
    }
  }

  const title = editing ? t("form.title.edit") : t("form.title.new");
  const back = (
    <Button asChild variant="outline">
      <Link href="/apps/">{t("form.back")}</Link>
    </Button>
  );

  const fe = (key: FieldKey) => errors[key];
  const errorKeys = Object.keys(errors) as FieldKey[];

  return (
    <div className="space-y-4" data-testid="app-form-page">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-card p-5 shadow-card">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          <p className="text-sm text-muted-foreground">{editing ? t("form.subtitle.edit") : t("form.subtitle.new")}</p>
        </div>
        {back}
      </div>

      <Notice notice={notice} onDismiss={clear} />

      {created ? (
        <WebhookSnippet appName={created.name} secret={created.secret} onDismiss={() => router.push("/apps/")} />
      ) : (
        <form onSubmit={submit} noValidate className="space-y-4" data-testid="app-form">
          {(formError || errorKeys.length > 0) && (
            <div role="alert" data-testid="form-errors" className="space-y-1 rounded-2xl bg-danger-soft px-4 py-3 text-sm text-danger-soft-foreground">
              {formError && <p>{formError}</p>}
              {errorKeys.length > 0 && (
                <>
                  <p>{t("form.summary")}</p>
                  <ul className="list-disc pl-5">
                    {errorKeys.map((k) => (
                      <li key={k}>
                        <a href={`#${fieldId(k)}`} className="underline">
                          {t(labelKey(k))}
                        </a>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          )}

          <div className="grid gap-5 rounded-card bg-card p-6 shadow-card">
            <Field id="f-name" label={t("form.name")} error={fe("name")}>
              <Input id="f-name" name="name" required autoComplete="off" value={v.name} onChange={(e) => set("name", e.target.value)} aria-invalid={fe("name") ? true : undefined} aria-describedby={describedBy("f-name", fe("name"))} />
            </Field>

            <Field id="f-secret" label={t("form.webhookSecret")} error={fe("webhookSecret")} hint={editing ? t("form.webhookSecret.editHint") : t("form.webhookSecret.createHint")}>
              <Input id="f-secret" name="webhookSecret" type="password" autoComplete="new-password" value={v.webhookSecret} onChange={(e) => set("webhookSecret", e.target.value)} aria-invalid={fe("webhookSecret") ? true : undefined} aria-describedby={describedBy("f-secret", fe("webhookSecret"), "x")} />
            </Field>

            <div className="grid gap-5 sm:grid-cols-2">
              <Field id="f-service" label={t("form.serviceName")} error={fe("serviceName")}>
                <Input id="f-service" name="serviceName" required autoComplete="off" value={v.serviceName} onChange={(e) => set("serviceName", e.target.value)} aria-invalid={fe("serviceName") ? true : undefined} aria-describedby={describedBy("f-service", fe("serviceName"))} />
              </Field>
              <Field id="f-binary" label={t("form.binaryPath")} error={fe("binaryPath")}>
                <Input id="f-binary" name="binaryPath" required autoComplete="off" value={v.binaryPath} onChange={(e) => set("binaryPath", e.target.value)} aria-invalid={fe("binaryPath") ? true : undefined} aria-describedby={describedBy("f-binary", fe("binaryPath"))} />
              </Field>
              <Field id="f-repo" label={t("form.githubRepo")} error={fe("githubRepo")} hint={t("form.githubRepo.hint")}>
                <Input id="f-repo" name="githubRepo" required autoComplete="off" value={v.githubRepo} onChange={(e) => set("githubRepo", e.target.value)} aria-invalid={fe("githubRepo") ? true : undefined} aria-describedby={describedBy("f-repo", fe("githubRepo"), "x")} />
              </Field>
              <Field id="f-artifact" label={t("form.artifactName")} error={fe("artifactName")}>
                <Input id="f-artifact" name="artifactName" required autoComplete="off" value={v.artifactName} onChange={(e) => set("artifactName", e.target.value)} aria-invalid={fe("artifactName") ? true : undefined} aria-describedby={describedBy("f-artifact", fe("artifactName"))} />
              </Field>
            </div>

            <EnvEditor status={envStatus} value={envText} onChange={setEnvText} onRetry={() => void envQ.refetch()} fieldError={fe("envVars")} />
            <ArgsEditor status={argsStatus} value={argsText} onChange={setArgsText} onRetry={() => void argsQ.refetch()} fieldError={fe("args")} />

            {editing && <SwitchRow id="f-enabled" label={t("form.enabled")} hint={t("form.enabled.hint")} checked={v.enabled} onChange={(c) => set("enabled", c)} />}

            <details className="rounded-2xl bg-secondary p-4">
              <summary className="cursor-pointer font-medium">{t("form.advanced")}</summary>
              <div className="mt-4 grid gap-5">
                <Field id="f-user" label={t("form.runAsUser")} error={fe("runAsUser")} hint={t("form.runAsUser.hint")}>
                  <Input id="f-user" name="runAsUser" autoComplete="off" value={v.runAsUser} onChange={(e) => set("runAsUser", e.target.value)} aria-invalid={fe("runAsUser") ? true : undefined} aria-describedby={describedBy("f-user", fe("runAsUser"), "x")} />
                </Field>
                <SwitchRow id="f-hardening" label={t("form.hardening")} hint={t("form.hardening.hint")} checked={v.hardening} onChange={(c) => set("hardening", c)} />
                <Field id="f-health" label={t("form.healthUrl")} error={fe("healthUrl")} hint={t("form.healthUrl.hint")}>
                  <Input id="f-health" name="healthUrl" type="url" inputMode="url" autoComplete="off" value={v.healthUrl} onChange={(e) => set("healthUrl", e.target.value)} aria-invalid={fe("healthUrl") ? true : undefined} aria-describedby={describedBy("f-health", fe("healthUrl"), "x")} />
                </Field>
                <SwitchRow id="f-digest" label={t("form.requireDigest")} hint={t("form.requireDigest.hint")} checked={v.requireDigest} onChange={(c) => set("requireDigest", c)} />
              </div>
            </details>

            <div className="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <Button asChild variant="outline">
                <Link href="/apps/">{t("common.cancel")}</Link>
              </Button>
              <Button type="submit" disabled={busy}>
                {busy ? t("form.submitting") : editing ? t("form.submit.update") : t("form.submit.create")}
              </Button>
            </div>
          </div>
        </form>
      )}

      {editing && !created && (
        <>
          <section aria-labelledby="unit-title" className="flex flex-wrap items-center justify-between gap-4 rounded-card bg-card p-6 shadow-card">
            <div className="max-w-prose space-y-1">
              <h2 id="unit-title" className="text-xl font-semibold">
                {t("form.unit.title")}
              </h2>
              <p className="text-sm text-muted-foreground">{t("form.unit.desc")}</p>
            </div>
            <Button type="button" variant="outline" onClick={() => setUnitOpen(true)}>
              {t("apps.unit")}
            </Button>
          </section>
          {unitOpen && (
            <ServiceUnitDialog
              appId={appId}
              open
              onOpenChange={setUnitOpen}
              onApplied={() => success(t("apps.unit.done"))}
            />
          )}
          <WebhookReference appName={v.name} secret={v.webhookSecret} />
        </>
      )}
    </div>
  );
}

function fieldId(k: FieldKey): string {
  const map: Record<FieldKey, string> = { name: "f-name", binaryPath: "f-binary", serviceName: "f-service", githubRepo: "f-repo", artifactName: "f-artifact", webhookSecret: "f-secret", runAsUser: "f-user", healthUrl: "f-health", envVars: "env-vars", args: "service-args" };
  return map[k];
}

function labelKey(k: FieldKey): MessageKey {
  const map: Record<FieldKey, MessageKey> = { name: "form.name", binaryPath: "form.binaryPath", serviceName: "form.serviceName", githubRepo: "form.githubRepo", artifactName: "form.artifactName", webhookSecret: "form.webhookSecret", runAsUser: "form.runAsUser", healthUrl: "form.healthUrl", envVars: "form.env", args: "form.args" };
  return map[k];
}

/** Create: renders the empty form. Edit: loads the app first, then mounts the form ONCE. */
export function AppForm({ appId }: { appId?: string }) {
  const i18n = useI18n();
  const { t } = i18n;
  const app = useQuery({
    queryKey: queryKeys.app(appId ?? ""),
    queryFn: ({ signal }) => api.getApp(appId as string, { signal }),
    enabled: appId !== undefined,
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });

  if (appId === undefined) return <AppFormInner initial={null} />;
  if (app.isPending) return <p role="status" className="rounded-card bg-card p-6 shadow-card">{t("common.loading")}</p>;
  if (app.isError) {
    const notFound = app.error instanceof ApiError && app.error.status === 404;
    return (
      <div role="alert" className="space-y-3 rounded-card bg-card p-6 shadow-card" data-testid="form-load-error">
        <p>{notFound ? t("form.notFound") : t("form.loadError")}</p>
        {!notFound && <p className="text-sm text-muted-foreground">{describeError(app.error, i18n)}</p>}
        <div className="flex gap-2">
          {!notFound && (
            <Button type="button" onClick={() => void app.refetch()}>
              {t("common.retry")}
            </Button>
          )}
          <Button asChild variant="outline">
            <Link href="/apps/">{t("form.back")}</Link>
          </Button>
        </div>
      </div>
    );
  }
  return <AppFormInner key={appId} appId={appId} initial={app.data.app} />;
}
