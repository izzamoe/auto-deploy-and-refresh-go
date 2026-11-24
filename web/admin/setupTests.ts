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