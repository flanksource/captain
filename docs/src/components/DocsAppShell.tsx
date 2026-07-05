import type { ReactNode } from "react";
import { AppShell, type AppShellNavSection } from "@flanksource/clicky-ui/components";
import { RouterProvider, type RouterAdapter } from "@flanksource/clicky-ui/rpc";
import { docSections, isActivePath } from "../data/navigation";
import { ClickyProviders } from "./ClickyProviders";

type DocsAppShellProps = {
  currentPath: string;
  children: ReactNode;
};

export default function DocsAppShell({ currentPath, children }: DocsAppShellProps) {
  const navSections = docSections.map<AppShellNavSection>((section) => ({
    label: section.label,
    items: section.items.map((item) => ({
      key: item.href,
      label: item.label,
      to: item.href,
      active: isActivePath(currentPath, item.href),
      badge:
        item.status === "stub" ? (
          <span className="rounded border border-sidebar-border px-1.5 py-0.5 text-[10px] uppercase text-sidebar-foreground/65">
            next
          </span>
        ) : undefined,
    })),
  }));

  const router: RouterAdapter = {
    pathname: currentPath,
    navigate: (to, opts) => {
      if (typeof window === "undefined") return;
      if (opts?.replace) window.location.replace(to);
      else window.location.assign(to);
    },
    renderLink: ({ to, className, children: linkChildren, title, key }) => (
      <a key={key} href={to} className={className} title={title}>
        {linkChildren}
      </a>
    ),
  };

  return (
    <ClickyProviders>
      <RouterProvider adapter={router}>
        <AppShell
          className="h-screen min-h-screen"
          brand={<DocsBrand />}
          nav={<div className="text-sm font-medium text-foreground">Documentation</div>}
          actions={
            <a
              href="/prompts/"
              className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground no-underline hover:bg-muted hover:text-foreground"
            >
              Prompts
            </a>
          }
          navSections={navSections}
          collapsible={false}
          sidebarWidth={272}
          mobileSidebarLabel="Docs navigation"
          contentClassName="p-0"
        >
          {children}
        </AppShell>
      </RouterProvider>
    </ClickyProviders>
  );
}

function DocsBrand() {
  return (
    <a href="/" className="flex min-w-0 items-center gap-3 text-sidebar-foreground no-underline">
      <span className="grid size-8 shrink-0 place-items-center rounded-md border border-sidebar-border bg-sidebar-accent text-sm font-bold">
        C
      </span>
      <span className="min-w-0">
        <span className="block truncate text-sm font-semibold">Captain Docs</span>
        <span className="block truncate text-xs text-sidebar-foreground/65">
          Runtime docs
        </span>
      </span>
    </a>
  );
}

