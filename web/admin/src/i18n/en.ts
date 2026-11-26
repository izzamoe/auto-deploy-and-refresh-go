// English dictionary, COMPLETE (002 T072, FR-029): the per-namespace files under en/ joined, for
// the tests and the type check only. `Record<MessageKey, string>` keeps a missing or extra key a
// compile error (D14). The app never imports this file at runtime: each id namespace module
// registers the loader of its en/<ns>.ts, which runs only when the English UI is needed.
import type { MessageKey } from "./id";
import { enAccount } from "./en/account";
import { enApps } from "./en/apps";
import { enCore } from "./en/core";
import { enDashboard } from "./en/dashboard";
import { enForm } from "./en/form";
import { enHistory } from "./en/history";
import { enLogin } from "./en/login";
import { enNotifications } from "./en/notifications";
import { enPassword } from "./en/password";
import { enSettings } from "./en/settings";
import { enSyslogs } from "./en/syslogs";
import { enUnit } from "./en/unit";

export const en: Record<MessageKey, string> = {
  ...enCore,
  ...enDashboard,
  ...enApps,
  ...enUnit,
  ...enForm,
  ...enHistory,
  ...enSyslogs,
  ...enSettings,
  ...enAccount,
  ...enLogin,
  ...enNotifications,
  ...enPassword,
};
