import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  Button,
  InputField,
  Modal,
  Switch,
} from "@flanksource/clicky-ui/components";

import { enrollGitAgent, type GitAgentEnrollment } from "./sandboxData";

/**
 * Enrollment is a two-sided hand-off, so the modal is two steps rather than a
 * form that "creates" an agent: the supervisor mints a durable token, and the
 * operator runs the printed command on the agent host. Nothing exists on the
 * remote side until they do.
 */
export function GitAgentEnrollModal({
  open,
  backend,
  onClose,
  onEnrolled,
}: {
  open: boolean;
  backend: string;
  onClose: () => void;
  onEnrolled: () => void;
}) {
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [dryRun, setDryRun] = useState(false);
  const [result, setResult] = useState<GitAgentEnrollment | undefined>();

  const enroll = useMutation({
    mutationFn: () =>
      enrollGitAgent({
        backend,
        name: name.trim(),
        ...(endpoint.trim() ? { endpoint: endpoint.trim() } : {}),
        ...(dryRun ? { dryRun: true } : {}),
      }),
    onSuccess: (enrollment) => {
      setResult(enrollment);
      // A dry run records nothing, so there is no roster change to pick up.
      if (!enrollment.dryRun) onEnrolled();
    },
  });

  const reset = () => {
    setName("");
    setEndpoint("");
    setDryRun(false);
    setResult(undefined);
    enroll.reset();
    onClose();
  };

  return (
    <Modal
      open={open}
      onClose={reset}
      title={result ? "Run this on the agent host" : "Enroll a git-agent"}
      size="lg"
    >
      <div className="grid gap-density-4 p-density-4">
        {!result && (
          <>
            <label className="block space-y-1 text-xs text-muted-foreground">
              <span>Agent name</span>
              <InputField
                value={name}
                onChange={setName}
                placeholder="worker-01"
                autoFocus
              />
            </label>
            <label className="block space-y-1 text-xs text-muted-foreground">
              <span>
                Endpoint{" "}
                <span className="text-muted-foreground/70">
                  (optional — defaults to the backend&apos;s url)
                </span>
              </span>
              <InputField
                value={endpoint}
                onChange={setEndpoint}
                placeholder="ssh://supervisor:7422"
              />
            </label>
            <div className="text-xs text-muted-foreground">
              <Switch
                checked={dryRun}
                onChange={setDryRun}
                label="Dry run — print the intended changes without writing them"
              />
            </div>
            {enroll.error && (
              <p role="alert" className="text-xs text-destructive">
                {enroll.error instanceof Error
                  ? enroll.error.message
                  : String(enroll.error)}
              </p>
            )}
            <div className="flex justify-end gap-density-2">
              <Button variant="ghost" onClick={reset}>
                Cancel
              </Button>
              <Button
                onClick={() => enroll.mutate()}
                disabled={!name.trim() || enroll.isPending}
              >
                {enroll.isPending ? "Enrolling…" : "Enroll"}
              </Button>
            </div>
          </>
        )}

        {result && (
          <>
            {result.dryRun ? (
              <p className="text-xs text-muted-foreground">
                Dry run — nothing was written and no token was minted.
              </p>
            ) : (
              <>
                <p className="text-xs text-muted-foreground">
                  The token is shown once and cannot be recovered afterwards. It
                  stays valid{" "}
                  {result.expires ? (
                    <>
                      until{" "}
                      <strong>
                        {new Date(result.expires).toLocaleString()}
                      </strong>
                    </>
                  ) : (
                    "until it is revoked"
                  )}
                  , so a restarting agent re-presents the same one. Running this
                  establishes trust both ways: the supervisor learns the
                  agent&apos;s endpoint and host key, and the agent authorizes the
                  supervisor&apos;s dispatch key.
                </p>
                <CopyableCommand command={result.joinCommand} />
                <dl className="grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 text-xs">
                  {result.tokenId && (
                    <>
                      <dt className="text-muted-foreground">Token</dt>
                      <dd className="font-mono break-all">{result.tokenId}</dd>
                    </>
                  )}
                  <dt className="text-muted-foreground">Host key</dt>
                  <dd className="font-mono break-all">{result.hostFingerprint}</dd>
                  <dt className="text-muted-foreground">Dispatch key</dt>
                  <dd className="font-mono break-all">{result.dispatchKey}</dd>
                </dl>
              </>
            )}
            <div className="flex justify-end">
              <Button onClick={reset}>Done</Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="grid gap-density-2">
      <pre className="overflow-x-auto rounded-md border border-border bg-muted p-density-3 font-mono text-xs">
        {command}
      </pre>
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            void navigator.clipboard?.writeText(command);
            setCopied(true);
          }}
        >
          {copied ? "Copied" : "Copy command"}
        </Button>
      </div>
    </div>
  );
}
