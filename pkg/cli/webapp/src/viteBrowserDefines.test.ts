import { describe, expect, it } from "vitest";
import { browserDefines } from "../viteBrowserDefines";

describe("browserDefines", () => {
  it("replaces Node environment checks in browser dependencies", () => {
    expect(browserDefines("serve")).toEqual({
      "process.env.NODE_ENV": JSON.stringify("development"),
    });
    expect(browserDefines("build")).toEqual({
      "process.env.NODE_ENV": JSON.stringify("production"),
    });
  });
});
