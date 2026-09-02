export function browserDefines(
  command: "serve" | "build",
  clickyDefines: Record<string, string | boolean> = {},
) {
  return {
    "process.env.NODE_ENV": JSON.stringify(
      command === "serve" ? "development" : "production",
    ),
    ...clickyDefines,
  };
}
