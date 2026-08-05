import {
  Combobox,
  DensitySwitcher,
  ThemeSwitcher,
} from "@flanksource/clicky-ui/components";
import { useQuery } from "@tanstack/react-query";
import { DEFAULT_DB_CONTEXT, fetchDbContexts, setDbContext } from "./dbContext";
import {
  ALL_PROJECTS_SCOPE,
  fetchProjectOptions,
  type ProjectOption,
  type ProjectScope,
} from "./sessionData";
import { parseDbContextOption, projectOptions } from "./shellHelpers";

/** Right-aligned top-bar cluster shared across every route. */
export function ShellActions({
  projectScope = ALL_PROJECTS_SCOPE,
  onProjectScopeChange,
}: {
  projectScope?: ProjectScope;
  onProjectScopeChange?: (scope: ProjectScope) => void;
}) {
  const projectsQuery = useQuery({
    queryKey: ["project-options"],
    queryFn: fetchProjectOptions,
    staleTime: 30_000,
  });
  const contextsQuery = useQuery({
    queryKey: ["db-contexts"],
    queryFn: fetchDbContexts,
    staleTime: 60_000,
  });
  const contexts = contextsQuery.data?.contexts ?? EMPTY_CONTEXTS;
  const activeContext = contextsQuery.data?.active ?? DEFAULT_DB_CONTEXT;
  const active = contexts.find((context) => context.name === activeContext);
  const options = projectOptions(
    projectScope,
    projectsQuery.data?.projects ?? EMPTY_PROJECTS,
    contexts,
    activeContext,
  );

  return (
    <>
      {active && !active.default ? (
        <span
          className="rounded border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-xs font-medium text-amber-700 dark:text-amber-300"
          title={active.source || active.name}
        >
          {active.label} · read-only
        </span>
      ) : null}
      <Combobox
        className="w-64 max-w-[40vw]"
        value={projectScope || ALL_PROJECTS_SCOPE}
        onChange={(value) => {
          const context = parseDbContextOption(value);
          if (context) {
            setDbContext(context);
            return;
          }
          onProjectScopeChange?.(value || ALL_PROJECTS_SCOPE);
        }}
        options={options}
        label="Project"
        ariaLabel="Project"
        placeholder="All projects"
        allowCustomValue={false}
        loading={projectsQuery.isFetching}
        size="sm"
      />
      <ThemeSwitcher />
      <DensitySwitcher />
    </>
  );
}

const EMPTY_PROJECTS: ProjectOption[] = [];
const EMPTY_CONTEXTS: NonNullable<
  Awaited<ReturnType<typeof fetchDbContexts>>
>["contexts"] = [];
