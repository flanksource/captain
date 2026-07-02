import {
  DensitySwitcher,
  ThemeSwitcher,
  type AppShellNavSection,
} from "@flanksource/clicky-ui/components";
import {
  UiActivity,
  UiFileText,
  UiHistory,
  UiRobotAi,
  UiServer,
} from "@flanksource/clicky-ui/data";

export type PrimaryRoute = "dashboard" | "agent" | "sessions" | "prompts" | "operations";

/** localStorage key persisting the AppShell sidebar rail's collapsed state. */
export const CAPTAIN_SIDEBAR_COLLAPSE_KEY = "captain:sidebar:collapsed";

/** Sidebar rail sections for the Captain shell, with `active` set for the current route. */
export function captainNavSections(active: PrimaryRoute): AppShellNavSection[] {
  return [
    {
      items: [
        { key: "dashboard", label: "Dashboard", to: "/", icon: UiActivity, active: active === "dashboard" },
        { key: "agent", label: "Agent", to: "/agent", icon: UiRobotAi, active: active === "agent" },
        { key: "sessions", label: "Sessions", to: "/sessions", icon: UiHistory, active: active === "sessions" },
        { key: "prompts", label: "Prompts", to: "/prompts", icon: UiFileText, active: active === "prompts" },
        { key: "operations", label: "Operations", to: "/operations", icon: UiServer, active: active === "operations" },
      ],
    },
  ];
}

/** Right-aligned top-bar cluster shared across every route. */
export function ShellActions() {
  return (
    <>
      <ThemeSwitcher />
      <DensitySwitcher />
    </>
  );
}
