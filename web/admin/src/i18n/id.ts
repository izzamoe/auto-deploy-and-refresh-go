// Indonesian dictionary: the SOURCE OF TRUTH for message keys (FR-104, D14). en.ts is typed
// `Record<MessageKey, string>`, so a missing or extra key there is a compile error, and
// i18n.test.ts re-checks key parity, `{placeholder}` parity and the OpenAPI ErrorCode map.
//
// Flat dotted keys; `{name}` is a placeholder filled by `t(key, { name })`. Pages add their
// own sections here as they are built; components never contain user-visible literals.
import { idCore } from "./id/core";
import { idDashboard } from "./id/dashboard";
import { idApps } from "./id/apps";
import { idUnit } from "./id/unit";
import { idForm } from "./id/form";

// The COMPLETE dictionary: the source of the `MessageKey` type and of the tests. The app itself
// never imports this file at runtime (it would pull every page's text into the shared bundle).
import { idHistory } from "./id/history";

import { idSyslogs } from "./id/syslogs";

import { idSettings } from "./id/settings";

export const id = {
  ...idCore,
  ...idDashboard,
  ...idApps,
  ...idUnit,
  ...idForm,
  ...idHistory,
  ...idSyslogs,
  ...idSettings,
} as const;

export type MessageKey = keyof typeof id;
