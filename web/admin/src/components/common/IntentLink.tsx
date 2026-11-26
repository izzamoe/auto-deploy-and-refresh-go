"use client";

// 002 T071 (FR-028, Q-8=A): a route is prefetched only on intent. `next/link` would prefetch
// every link that scrolls into view (7-10 JS chunks + 14-20 RSC payloads per cold load,
// research K9); `prefetch={false}` turns that off (viewport AND hover), and this link calls
// `router.prefetch(href)` itself on pointerenter / focus / touchstart, once per href.
import Link from "next/link";
import { useRouter } from "next/navigation";
import { forwardRef } from "react";
import type { ComponentPropsWithoutRef } from "react";

type Router = ReturnType<typeof useRouter>;

const prefetched = new Set<string>();

/** Tests only: forget which hrefs were prefetched. */
export function resetIntentPrefetchForTests() {
  prefetched.clear();
}

/** Prefetches `href` once (no-op outside the app router, e.g. isolated component tests). */
export function prefetchOnIntent(router: Router | null, href: string) {
  if (!router || prefetched.has(href)) return;
  prefetched.add(href);
  router.prefetch(href);
}

function useOptionalRouter(): Router | null {
  // useRouter throws when no app router is mounted (isolated component tests); a link must
  // still render there, it just has nothing to prefetch with.
  try {
    return useRouter();
  } catch {
    return null;
  }
}

type Props = Omit<ComponentPropsWithoutRef<typeof Link>, "prefetch" | "href"> & { href: string };

export const IntentLink = forwardRef<HTMLAnchorElement, Props>(function IntentLink({ href, onPointerEnter, onFocus, onTouchStart, ...props }, ref) {
  const router = useOptionalRouter();
  return (
    <Link
      ref={ref}
      href={href}
      prefetch={false}
      onPointerEnter={(e) => {
        onPointerEnter?.(e);
        prefetchOnIntent(router, href);
      }}
      onFocus={(e) => {
        onFocus?.(e);
        prefetchOnIntent(router, href);
      }}
      onTouchStart={(e) => {
        onTouchStart?.(e);
        prefetchOnIntent(router, href);
      }}
      {...props}
    />
  );
});
