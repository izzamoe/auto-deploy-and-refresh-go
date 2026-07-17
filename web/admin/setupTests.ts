import '@testing-library/jest-dom';

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