import { describe, expect, it } from "vitest";
import {
  formatTimingMs,
  parseServerTiming,
  phaseLabel,
} from "./serverTiming";

describe("server timing", () => {
  it("parses the detailed RPC and session phases", () => {
    expect(
      parseServerTiming(
        "total;dur=12035.3, command;dur=35.4, format;dur=11990.2, database;dur=0.2, lookup;dur=8.1, hydrate;dur=26.7, parse;dur=24.6, prompt_runs;dur=1.1",
      ),
    ).toEqual([
      { name: "total", dur: 12035.3 },
      { name: "command", dur: 35.4 },
      { name: "format", dur: 11990.2 },
      { name: "database", dur: 0.2 },
      { name: "lookup", dur: 8.1 },
      { name: "hydrate", dur: 26.7 },
      { name: "parse", dur: 24.6 },
      { name: "prompt_runs", dur: 1.1 },
    ]);
  });

  it("uses friendly labels and preserves explicit descriptions", () => {
    expect(phaseLabel({ name: "command", dur: 1 })).toBe("Command");
    expect(phaseLabel({ name: "format", dur: 1 })).toBe("Format response");
    expect(phaseLabel({ name: "database", dur: 1 })).toBe("Database");
    expect(phaseLabel({ name: "lookup", dur: 1 })).toBe("Session lookup");
    expect(phaseLabel({ name: "hydrate", dur: 1 })).toBe("Hydrate sessions");
    expect(phaseLabel({ name: "prompt_runs", dur: 1 })).toBe("Prompt runs");
    expect(phaseLabel({ name: "custom", desc: "Custom phase", dur: 1 })).toBe(
      "Custom phase",
    );
    expect(phaseLabel({ name: "unknown", dur: 1 })).toBe("unknown");
  });

  it("formats sub-second and multi-second durations", () => {
    expect(formatTimingMs(3.25)).toBe("3.3 ms");
    expect(formatTimingMs(125)).toBe("125 ms");
    expect(formatTimingMs(12035.3)).toBe("12.0 s");
  });
});
