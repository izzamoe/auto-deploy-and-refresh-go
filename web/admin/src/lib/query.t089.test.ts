// T089 CR-1 (independent code review): an app.upserted or a mutation response without the
// last-deploy fields keeps them in the cache (merge, never replace).
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { queryKeys, upsertAppInCache } from "@/lib/query";

const ROW = (over: Record<string, unknown> = {}) => ({
  id: "a1", name: "web", binaryPath: "/b", serviceName: "web.service", githubRepo: "o/r", artifactName: "x", enabled: true,
  runAsUser: "", hardening: false, healthUrl: "", requireDigest: true,
  createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-25T10:00:00Z", ...over,
});
const LAST = { lastDeployTag: "v1.2.3", lastDeployStatus: "succeeded", lastDeployTime: "2026-11-25T09:00:00Z", lastJobId: "j9", lastJobStatus: "succeeded", lastDownloadBytes: 1024, lastDownloadSpeedBps: 512 };

describe("T089 CR-1: upsertAppInCache merges, it never wipes the last deploy", () => {
  it("an app.upserted / toggle response without last* keeps lastJobId and lastDeployTag in the list and the app entry", () => {
    const client = new QueryClient();
    client.setQueryData(queryKeys.apps, { apps: [ROW(LAST)] });
    client.setQueryData(queryKeys.app("a1"), { app: ROW(LAST) });
    // e.g. the toggle response (appResponse: no last* fields)
    upsertAppInCache(client, ROW({ enabled: false, updatedAt: "2026-11-25T11:00:00Z" }) as never);
    const row = (client.getQueryData(queryKeys.apps) as { apps: Record<string, unknown>[] }).apps[0];
    expect(row.enabled).toBe(false);
    expect(row.lastJobId).toBe("j9");
    expect(row.lastDeployTag).toBe("v1.2.3");
    expect(row.lastJobStatus).toBe("succeeded");
    expect(row.lastDeployTime).toBe("2026-11-25T09:00:00Z");
    const one = (client.getQueryData(queryKeys.app("a1")) as { app: Record<string, unknown> }).app;
    expect(one.enabled).toBe(false);
    expect(one.lastJobId).toBe("j9");
    expect(one.lastDeployTag).toBe("v1.2.3");
  });

  it("a row WITH newer last* fields still replaces them; an older updatedAt still never wins", () => {
    const client = new QueryClient();
    client.setQueryData(queryKeys.apps, { apps: [ROW(LAST)] });
    upsertAppInCache(client, ROW({ ...LAST, lastJobId: "j10", lastDeployTag: "v1.2.4", updatedAt: "2026-11-25T12:00:00Z" }) as never);
    let row = (client.getQueryData(queryKeys.apps) as { apps: Record<string, unknown>[] }).apps[0];
    expect(row.lastJobId).toBe("j10");
    expect(row.lastDeployTag).toBe("v1.2.4");
    upsertAppInCache(client, ROW({ enabled: false, updatedAt: "2026-11-25T08:00:00Z" }) as never);
    row = (client.getQueryData(queryKeys.apps) as { apps: Record<string, unknown>[] }).apps[0];
    expect(row.enabled).toBe(true);
  });
});
