import { describe, expect, it } from "vitest";
import type { DbContextOption } from "./dbContext";
import {
  captainNavSections,
  parseDbContextOption,
  projectOptions,
} from "./shellHelpers";

const DEFAULT_CONTEXT: DbContextOption = {
  name: "default",
  label: "captain embedded database",
  source: "captain embedded database",
  dsn: "postgres://postgres:***@localhost:5433/captain",
  default: true,
  readOnly: false,
};

const PROD_CONTEXT: DbContextOption = {
  name: "prod",
  label: "Production",
  source: "~/.config/gavel/db.json#prod",
  dsn: "postgres://reader:***@prod:5432/gavel",
  default: false,
  readOnly: true,
};

describe("captainNavSections", () => {
  it("exposes the full whoami route as an active primary destination", () => {
    const items = captainNavSections("whoami")[0]?.items ?? [];
    const whoami = items.find((item) => item.key === "whoami");

    expect(whoami).toMatchObject({
      label: "Whoami",
      to: "/whoami",
      active: true,
    });
  });

  it("lists the runtime profiles library right after prompts and marks it active", () => {
    const items = captainNavSections("runtime-profiles")[0]?.items ?? [];
    const keys = items.map((item) => item.key);

    expect(keys.indexOf("runtime-profiles")).toBe(keys.indexOf("prompts") + 1);
    expect(items.find((item) => item.key === "runtime-profiles")).toMatchObject({
      label: "Runtime profiles",
      to: "/runtime-profiles",
      active: true,
    });
    expect(items.filter((item) => item.active)).toHaveLength(1);
  });
});

describe("projectOptions", () => {
  it("omits the database group when only the monitored database is configured", () => {
    const options = projectOptions("all", [], [DEFAULT_CONTEXT], "default");

    expect(options.filter((option) => option.group === "Database")).toEqual([]);
  });

  it("lists each database context and marks the active one", () => {
    const options = projectOptions("all", [], [DEFAULT_CONTEXT, PROD_CONTEXT], "prod");

    expect(options.filter((option) => option.group === "Database")).toEqual([
      {
        value: "ctx:default",
        label: "captain embedded database",
        title: "captain embedded database",
        group: "Database",
      },
      {
        value: "ctx:prod",
        label: "✓ Production (read-only)",
        title: "~/.config/gavel/db.json#prod",
        group: "Database",
      },
    ]);
  });

  it("keeps project entries alongside the database group", () => {
    const options = projectOptions(
      "all",
      [{ value: "captain", label: "captain", path: "/src/captain" }],
      [DEFAULT_CONTEXT, PROD_CONTEXT],
      "default",
    );

    expect(options.map((option) => option.group)).toEqual([
      "Scope",
      "Projects",
      "Database",
      "Database",
    ]);
  });
});

describe("parseDbContextOption", () => {
  it("extracts the context name from a database entry", () => {
    expect(parseDbContextOption("ctx:prod")).toBe("prod");
  });

  it("returns null for a project entry", () => {
    expect(parseDbContextOption("captain")).toBeNull();
  });
});
