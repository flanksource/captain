import { describe, expect, it } from "vitest";
import { captainNavSections } from "./shellHelpers";

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
});
