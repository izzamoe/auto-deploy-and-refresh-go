"use client";

import { useEffect } from "react";

const ID = /^[A-Za-z0-9_-]{1,128}$/;

/**
 * Maps a pre-migration hash route (`#/apps/x/edit`) to the new path, or null when the hash
 * is not a legacy route (so nothing happens). Pure: the component only applies the result.
 * Only the path and, for history, `appId` are read; nothing else from the URL is trusted.
 */
export function mapLegacyHash(hash: string): string | null {
  if (!hash.startsWith("#/")) return null;
  const [route, query = ""] = hash.slice(1).split("?", 2);
  const parts = route.split("/").filter(Boolean);

  if (parts.length === 0) return "/admin/";
  switch (parts[0]) {
    case "apps": {
      if (parts.length === 1) return "/admin/apps/";
      if (parts[1] === "new") return "/admin/apps/new/";
      if (parts[2] === "edit" && ID.test(parts[1])) return `/admin/apps/edit/?id=${encodeURIComponent(parts[1])}`;
      return "/admin/apps/";
    }
    case "history": {
      const appId = new URLSearchParams(query).get("appId");
      return appId && ID.test(appId) ? `/admin/history/?appId=${encodeURIComponent(appId)}` : "/admin/history/";
    }
    case "account":
      return "/admin/account/";
    case "telegram":
      return "/admin/settings/telegram/";
    case "github":
      return "/admin/settings/github/";
    case "system-logs":
      return "/admin/logs/";
    default:
      return "/admin/";
  }
}

/** Old bookmarks (`/admin/#/apps/x/edit`) keep working: replaces the URL once, no history entry. */
export function LegacyHashRedirect() {
  useEffect(() => {
    const target = mapLegacyHash(window.location.hash);
    if (target) window.location.replace(target);
  }, []);
  return null;
}
