import type { AdapterSchemaDocument } from "./adapterSchemas";

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

export function AdapterSchemaProvenance({ document }: { document: AdapterSchemaDocument }) {
  const schema = document.schema;
  const resource = record(schema["x-captain-resource"]);
  const inventory = record(schema["x-captain-inventory"]);
  const adapter = record(schema["x-captain-adapter"]);
  const sources = Array.isArray(adapter.sources) ? adapter.sources : [];
  const versions = [resource.version, inventory.version, ...sources.map((source: unknown) => record(source).version)]
    .filter((version): version is string => typeof version === "string" && version.length > 0);
  const rawLinks = record(schema["x-help"]).links;
  if (rawLinks !== undefined && !Array.isArray(rawLinks)) throw new Error("Schema help links must be an array.");
  const links = (rawLinks ?? []).map((value: unknown) => {
    const link = record(value);
    if (typeof link.href !== "string" || typeof link.label !== "string") throw new Error("Schema help links require href and label strings.");
    return { href: link.href, label: link.label };
  });
  return <div className="space-y-1 break-words text-xs text-muted-foreground">
    {versions.length > 0 && <p>Source: {[...new Set(versions)].join(" · ")}</p>}
    {links.map((link) => {
      const url = new URL(link.href);
      if (!["http:", "https:"].includes(url.protocol)) throw new Error(`Unsupported documentation URL: ${link.href}`);
      return <a key={link.href} href={url.href} target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-2">{link.label}</a>;
    })}
  </div>;
}
