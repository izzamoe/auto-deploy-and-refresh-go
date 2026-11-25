// The /vitest entry registers the matchers with Vitest's expect AND augments its
// Assertion type, so tsc sees toHaveAttribute/toHaveTextContent.
import '@testing-library/jest-dom/vitest';

// jsdom does not implement ResizeObserver, which some Radix primitives (e.g. the
// Switch used on the edit form) reference on mount. Provide a no-op polyfill so
// components that depend on it can render under test.
if (!globalThis.ResizeObserver) {
	globalThis.ResizeObserver = class {
		observe() {}
		unobserve() {}
		disconnect() {}
	};
}
// jsdom has no matchMedia, which next-themes calls to resolve the system theme.
if (typeof window !== "undefined" && !window.matchMedia) {
	window.matchMedia = (query: string) =>
		({
			matches: false,
			media: query,
			onchange: null,
			addListener() {},
			removeListener() {},
			addEventListener() {},
			removeEventListener() {},
			dispatchEvent: () => false,
		}) as MediaQueryList;
}

// The English dictionary is a lazily loaded chunk in the app; tests register it up front so
// locale switching stays synchronous under act().
import { registerDictionary } from "@/i18n";
import { en } from "@/i18n/en";
registerDictionary("en", en);
