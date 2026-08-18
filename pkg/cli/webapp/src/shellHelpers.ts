import type {
  AppShellNavSection,
  ComboboxOption,
} from "@flanksource/clicky-ui/components";
import {
  UiActivity,
  UiBox,
  UiFileText,
  UiFingerprint,
  UiHistory,
  UiRobotAi,
  UiServer,
} from "@flanksource/clicky-ui/data";
import { DEFAULT_DB_CONTEXT, type DbContextOption } from "./dbContext";
import {
  ALL_PROJECTS_SCOPE,
  projectLabel,
  type ProjectOption,
  type ProjectScope,
} from "./sessionData";

export type PrimaryRoute =
  | "dashboard"
  | "agent"
  | "sessions"
  | "prompts"
  | "whoami"
  | "sandboxes"
  | "operations";

export const CAPTAIN_SIDEBAR_COLLAPSE_KEY = "captain:sidebar:collapsed";

const PROJECT_SCOPE_PARAM = "project";
const PROJECT_SCOPE_CHANGE_EVENT = "captain:project-scope-change";

/** Sidebar rail sections for the Captain shell, with `active` set for the current route. */
export function captainNavSections(
  active: PrimaryRoute,
  projectScope: ProjectScope = ALL_PROJECTS_SCOPE,
): AppShellNavSection[] {
  return [
    {
      items: [
        {
          key: "dashboard",
          label: "Dashboard",
          to: withProjectScope("/", projectScope),
          icon: UiActivity,
          active: active === "dashboard",
        },
        { key: "agent", label: "Agent", to: "/agent", icon: UiRobotAi, active: active === "agent" },
        {
          key: "whoami",
          label: "Whoami",
          to: "/whoami",
          icon: UiFingerprint,
          active: active === "whoami",
        },
        {
          key: "sessions",
          label: "Sessions",
          to: withProjectScope("/sessions", projectScope),
          icon: UiHistory,
          active: active === "sessions",
        },
        { key: "prompts", label: "Prompts", to: "/prompts", icon: UiFileText, active: active === "prompts" },
        {
          key: "sandboxes",
          label: "Sandboxes",
          to: "/sandboxes",
          icon: UiBox,
          active: active === "sandboxes",
        },
        {
          key: "operations",
          label: "Operations",
          to: "/operations",
          icon: UiServer,
          active: active === "operations",
        },
      ],
    },
  ];
}

/** Prefix distinguishing a database-context entry from a project entry. */
export const DB_CONTEXT_OPTION_PREFIX = "ctx:";

export function projectOptions(
  projectScope: ProjectScope,
  projects: ProjectOption[],
  contexts: DbContextOption[] = [],
  activeContext: string = DEFAULT_DB_CONTEXT,
): ComboboxOption[] {
  const options: ComboboxOption[] = [
    { value: ALL_PROJECTS_SCOPE, label: "All projects", group: "Scope" },
  ];
  const seen = new Set<string>([ALL_PROJECTS_SCOPE]);
  for (const project of projects) {
    seen.add(project.value);
    options.push({
      value: project.value,
      label: project.label,
      title: project.path,
      group: "Projects",
    });
  }
  if (projectScope && projectScope !== ALL_PROJECTS_SCOPE && !seen.has(projectScope)) {
    options.push({
      value: projectScope,
      label: projectLabel(projectScope),
      title: projectScope,
      group: "Selected",
    });
  }
  // Database contexts are a second, orthogonal axis on the same control: only
  // one is active at a time, and picking one never clears the project scope.
  if (contexts.length > 1) {
    for (const context of contexts) {
      const active = context.name === activeContext;
      options.push({
        value: `${DB_CONTEXT_OPTION_PREFIX}${context.name}`,
        label: `${active ? "✓ " : ""}${context.label}${context.readOnly ? " (read-only)" : ""}`,
        title: context.source || context.name,
        group: "Database",
      });
    }
  }
  return options;
}

/** Returns the context name a picker entry selects, or null for a project entry. */
export function parseDbContextOption(value: string): string | null {
  return value.startsWith(DB_CONTEXT_OPTION_PREFIX)
    ? value.slice(DB_CONTEXT_OPTION_PREFIX.length)
    : null;
}

export function withProjectScope(path: string, projectScope: ProjectScope) {
  const [pathname, rawSearch = ""] = path.split("?");
  const params = new URLSearchParams(rawSearch);
  if (!projectScope || projectScope === ALL_PROJECTS_SCOPE) {
    params.delete(PROJECT_SCOPE_PARAM);
  } else {
    params.set(PROJECT_SCOPE_PARAM, projectScope);
  }
  const search = params.toString();
  return `${pathname}${search ? `?${search}` : ""}`;
}

export function getProjectScopeSnapshot(): ProjectScope {
  if (typeof window === "undefined") return ALL_PROJECTS_SCOPE;
  return parseProjectScope(window.location.search);
}

export function subscribeProjectScope(listener: () => void) {
  if (typeof window === "undefined") return () => undefined;
  window.addEventListener("popstate", listener);
  window.addEventListener(PROJECT_SCOPE_CHANGE_EVENT, listener);
  return () => {
    window.removeEventListener("popstate", listener);
    window.removeEventListener(PROJECT_SCOPE_CHANGE_EVENT, listener);
  };
}

export function setProjectScopeInLocation(
  projectScope: ProjectScope,
  navigate: (to: string, opts?: { replace?: boolean }) => void,
  pathname: string,
  search: string,
) {
  const nextScope = projectScope || ALL_PROJECTS_SCOPE;
  navigate(withProjectScope(`${pathname}${search}`, nextScope), { replace: true });
  notifyProjectScopeChanged();
}

export function parseProjectScope(search: string): ProjectScope {
  const value = new URLSearchParams(search).get(PROJECT_SCOPE_PARAM)?.trim();
  return value && value !== ALL_PROJECTS_SCOPE ? value : ALL_PROJECTS_SCOPE;
}

function notifyProjectScopeChanged() {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(PROJECT_SCOPE_CHANGE_EVENT));
}
