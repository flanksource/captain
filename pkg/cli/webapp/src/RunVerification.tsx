import { VerificationResults } from "@flanksource/clicky-ui/data";
import { parseVerifyFrame, type VerifyFrame } from "./types/verifyReport";

export function RunVerification({
  frame,
  storedReport,
  title = "Verification",
}: {
  frame?: VerifyFrame | null;
  storedReport?: unknown;
  title?: string;
}) {
  let verification = frame;
  if (!verification && storedReport != null) {
    try {
      verification = parseVerifyFrame({ report: storedReport, done: true });
    } catch (error) {
      return (
        <div role="alert" className="p-density-3 text-sm text-destructive">
          Invalid stored verification report: {error instanceof Error ? error.message : String(error)}
        </div>
      );
    }
  }
  if (!verification?.report) return null;
  return (
    <section aria-label={title} className="shrink-0 overflow-hidden rounded-md border border-border">
      <VerificationResults report={verification.report} done={verification.done} title={title} />
    </section>
  );
}
