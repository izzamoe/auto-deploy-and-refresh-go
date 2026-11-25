# Third-party notices (admin UI)

The admin UI is built from the packages below and embedded in the `auto-deploy` binary.
Versions are pinned in the root `package.json`; licences are those declared by each package.

## Font: DM Sans (self-hosted)

- Package: `@fontsource-variable/dm-sans` 5.3.0 (variable font files, `latin` and `latin-ext` subsets).
- Copyright 2014 The DM Sans Project Authors (https://github.com/googlefonts/dm-fonts).
- Licence: SIL Open Font License, Version 1.1 (http://scripts.sil.org/OFL), the full text is
  shipped with the package as `node_modules/@fontsource-variable/dm-sans/LICENSE`.
- The font is served from the same origin (`/admin/_next/static/media/*.woff2`); no CDN is used.

## Runtime libraries

| Package | Licence |
| --- | --- |
| next, react, react-dom | MIT |
| @tanstack/react-query | MIT |
| @radix-ui/* | MIT |
| lucide-react (icons) | ISC |
| class-variance-authority | Apache-2.0 |
| clsx, tailwind-merge, tw-animate-css, next-themes, sonner | MIT |

## Build and test tooling (not shipped)

tailwindcss and @tailwindcss/postcss (MIT), typescript (Apache-2.0), vitest and
@testing-library/* (MIT), @playwright/test (Apache-2.0), @axe-core/playwright (MPL-2.0).

## Design reference

`reference/design/ref-dashboard-green.png` was used as a style reference only (colour family,
radii, layout rhythm). No image, avatar, illustration or logo from it is reproduced; the
`public/logo.svg` mark and any illustration are original to this project.
