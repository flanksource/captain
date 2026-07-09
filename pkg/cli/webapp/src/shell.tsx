import {
  Combobox,
  DensitySwitcher,
  ThemeSwitcher,
} from "@flanksource/clicky-ui/components";
import { useQuery } from "@tanstack/react-query";
import {
  ALL_PROJECTS_SCOPE,
  fetchProjectOptions,
  type ProjectOption,
  type ProjectScope,
} from "./sessionData";
import { projectOptions } from "./shellHelpers";

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
  const options = projectOptions(projectScope, projectsQuery.data?.projects ?? EMPTY_PROJECTS);

  return (
    <>
      <Combobox
        className="w-64 max-w-[40vw]"
        value={projectScope || ALL_PROJECTS_SCOPE}
        onChange={(value) => onProjectScopeChange?.(value || ALL_PROJECTS_SCOPE)}
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
