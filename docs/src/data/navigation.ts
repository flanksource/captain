export type DocItem = {
  label: string;
  href: string;
  description?: string;
  status?: "ready" | "stub";
};

export type DocSection = {
  label: string;
  items: DocItem[];
};

export const docSections: DocSection[] = [
  {
    label: "Start",
    items: [
      {
        label: "Overview",
        href: "/",
        description: "Documentation map for Captain.",
        status: "ready",
      },
    ],
  },
  {
    label: "Prompts",
    items: [
      {
        label: "Prompts Overview",
        href: "/prompts/",
        description: "Prompt engine responsibilities and data flow.",
        status: "ready",
      },
      {
        label: "Prompt Format",
        href: "/prompts/format/",
        description: ".prompt frontmatter, roles, and variables.",
        status: "ready",
      },
      {
        label: "Runtime Overlay",
        href: "/prompts/runtime/",
        description: "Spec, model, permissions, memory, budget, and setup.",
        status: "ready",
      },
      {
        label: "Sources and API",
        href: "/prompts/sources-api/",
        description: "Prompt source discovery, render API, and run stream.",
        status: "ready",
      },
    ],
  },
  {
    label: "Todos",
    items: [
      {
        label: "Todo Execution",
        href: "/todos/",
        description: "How gavel drives Captain's agent runtime to execute TODOs.",
        status: "ready",
      },
      {
        label: "Todo Format",
        href: "/todos/format/",
        description: "Providers, file schema, statuses, and creation paths.",
        status: "ready",
      },
      {
        label: "Execution and Verification",
        href: "/todos/execution/",
        description: "Run modes, drivers, check loop, plan review, and AI verify.",
        status: "ready",
      },
    ],
  },
  {
    label: "Next Sections",
    items: [
      {
        label: "Serve",
        href: "/serve/",
        description: "Embedded API and web UI.",
        status: "stub",
      },
      {
        label: "AI Agents",
        href: "/agents/",
        description: "Agent loop, verifiers, worktrees, and judges.",
        status: "stub",
      },
      {
        label: "Sessions",
        href: "/sessions/",
        description: "Claude and Codex history surfaces.",
        status: "stub",
      },
      {
        label: "Configuration",
        href: "/configuration/",
        description: "Saved defaults and runtime toggles.",
        status: "stub",
      },
      {
        label: "Fixtures",
        href: "/fixtures/",
        description: "Benchmark fixtures and evidence reports.",
        status: "stub",
      },
    ],
  },
];

export function isActivePath(currentPath: string, href: string) {
  const normalizedCurrent = normalizePath(currentPath);
  const normalizedHref = normalizePath(href);
  return normalizedCurrent === normalizedHref;
}

function normalizePath(path: string) {
  if (path === "") return "/";
  if (path === "/") return "/";
  return path.endsWith("/") ? path : `${path}/`;
}

