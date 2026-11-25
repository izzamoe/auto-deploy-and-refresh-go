// next.config.ts sets basePath "/admin". next/link and the router add it themselves; plain
// URLs (images, window.location) do not, so they go through these constants.
export const BASE_PATH = "/admin";
export const LOGO_SRC = `${BASE_PATH}/logo.svg`;
