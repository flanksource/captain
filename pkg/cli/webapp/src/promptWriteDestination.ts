import type { PromptWriteSource } from "./PromptWriteModal";

/** Mirrors captain's slugPromptName: lowercase, runs of non-alphanumerics become one dash. */
export function slugPromptName(name: string) {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** Mirrors captain's normalizeWriteRelPath: explicit path wins, else the slugged name, always `.prompt`. */
export function promptWriteRelPath(relPath: string, name: string) {
  const rel = relPath.trim() || slugPromptName(name);
  if (!rel) return "";
  return rel.endsWith(".prompt") ? rel : `${rel}.prompt`;
}

/** The file a create/save-as will produce, shown before the user commits to it. */
export function promptWriteDestination(
  source: PromptWriteSource | undefined,
  relPath: string,
  name: string,
) {
  const rel = promptWriteRelPath(relPath, name);
  if (!rel) return "";
  const base = source?.root ?? source?.label;
  return base ? `${base.replace(/\/+$/, "")}/${rel}` : rel;
}
