import type { ReactNode } from "react";

export function StateMessage({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "neutral" | "error";
}) {
  const classes = tone === "error"
    ? "border-destructive/30 bg-destructive/10 text-destructive"
    : "border-border bg-muted/30 text-muted-foreground";
  return <div className={`rounded-md border px-density-4 py-density-3 text-sm ${classes}`}>{children}</div>;
}
