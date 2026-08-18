import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Button, FormErrorSummary, Modal } from "@flanksource/clicky-ui/components";

import { DeployForm } from "./GitAgentDeployForm";
import { DeployedSummary, PreviewPlan } from "./GitAgentDeployResult";
import { blockerSummary, deployBlockers } from "./gitAgentDeployValidation";
import {
  deployGitAgent,
  fetchDeployPreflight,
  fetchNamespaces,
  updateGitAgent,
  type DeployPreflight,
  type DeployRequest,
  type DeployResult,
  type DeployTarget,
  type GitAgentDeployment,
} from "./sandboxData";

const TARGETS: Array<{ value: DeployTarget; label: string; blurb: string }> = [
  {
    value: "docker",
    label: "Docker",
    blurb: "A container on this machine's docker daemon.",
  },
  {
    value: "kubernetes",
    label: "Kubernetes",
    blurb: "A Deployment, Service and PVC in your kubeconfig's cluster.",
  },
];

/** The route fields, which belong to kubernetes and are refused on docker. */
const ROUTING_KEYS = [
  "domain",
  "ingressClass",
  "ingressIssuer",
  "ingressTlsSecret",
  "ingressAnnotation",
] as const satisfies ReadonlyArray<keyof DeployRequest>;

/**
 * Deploying is three steps because the middle one carries the risk.
 *
 * A git-agent needs two addresses pointing in opposite directions — one the
 * agent reaches the supervisor on, one the supervisor dispatches back to — and
 * getting either wrong produces an agent that enrolls, shows as healthy, and
 * fails at its first dispatch hours later. So the target is probed before the
 * form is usable, and the resolved plan is shown before anything is created.
 */
export function GitAgentDeployModal({
  open,
  backend,
  edit,
  onClose,
  onDeployed,
}: {
  open: boolean;
  backend: string;
  edit?: { name: string; deployment: GitAgentDeployment };
  onClose: () => void;
  onDeployed: () => void;
}) {
  const initial = initialRequest(edit);
  const editing = Boolean(edit);
  const [target, setTarget] = useState<DeployTarget>(initial.target);
  const [form, setForm] = useState<DeployRequest>(initial);
  const [preview, setPreview] = useState<DeployResult | undefined>();
  const [deployed, setDeployed] = useState<DeployResult | undefined>();

  const preflight = useQuery({
    queryKey: ["git-agent-preflight", backend, target, form.transport, form.kubeContext],
    queryFn: () =>
      fetchDeployPreflight({
        backend,
        target,
        transport: form.transport,
        kubeContext: form.kubeContext,
      }),
    enabled: open,
    // A mailbox started (or stopped) while the modal is open changes the answer,
    // and a stale "no live mailbox" would block a deploy that is now possible.
    staleTime: 0,
  });

  const ready = preflight.data?.ready ?? false;

  // The namespaces the cluster already has, so the field can offer them and say
  // whether a typed one would be created. Gated on the preflight: until it says
  // the cluster is reachable there is nothing to list, and asking anyway would
  // fail once per target switch for a form that is not even rendered yet.
  const namespaces = useQuery({
    // No kubeconfig context: the form does not offer one, so this reads the
    // current context — the same cluster the preflight probed.
    queryKey: ["git-agent-namespaces", form.kubeContext],
    queryFn: () => fetchNamespaces(form.kubeContext),
    enabled: open && target === "kubernetes" && ready,
    retry: false,
  });
  const knownNamespaces = useMemo(() => namespaces.data ?? [], [namespaces.data]);

  // Empty means the kubeconfig context's own namespace, which exists by virtue
  // of having been selected there. Only a name the cluster does not have is a
  // creation, and only once the list actually loaded — otherwise every namespace
  // would look new.
  const chosenNamespace = form.namespace?.trim() ?? "";
  const willCreateNamespace =
    target === "kubernetes" &&
    chosenNamespace !== "" &&
    knownNamespaces.length > 0 &&
    !knownNamespaces.includes(chosenNamespace);

  useEffect(() => {
    setForm((current) => ({
      ...current,
      target,
      // A route left over from kubernetes is not ignored on docker, it is
      // refused — "--domain needs --target kubernetes" — so switching away has
      // to clear it rather than hide it.
      ...(target === "kubernetes"
        ? {}
        : Object.fromEntries(ROUTING_KEYS.map((key) => [key, undefined]))),
    }));
    setPreview(undefined);
  }, [target]);

  const set = <K extends keyof DeployRequest>(key: K, value: DeployRequest[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const deploy = useMutation({
    mutationFn: (dryRun: boolean) => {
      const request: DeployRequest = {
        ...form,
        target,
        dryRun,
        // Derived at submit rather than stored: it is a fact about the cluster
        // and the typed name, so keeping a copy in the form would let the two
        // drift once either changes.
        createNamespace: editing ? false : willCreateNamespace,
      };
      return edit
        ? updateGitAgent(backend, edit.name, request)
        : deployGitAgent(backend, request);
    },
    onSuccess: (result) => {
      if (result.dryRun) {
        setPreview(result);
        return;
      }
      setDeployed(result);
      onDeployed();
    },
  });

  const reset = () => {
    const next = initialRequest(edit);
    setTarget(next.target);
    setForm(next);
    setPreview(undefined);
    setDeployed(undefined);
    deploy.reset();
    onClose();
  };

  const blockers = deployBlockers({ ...form, target }, preflight.data);
  const canSubmit = ready && Object.keys(blockers).length === 0;

  return (
    <Modal
      open={open}
      onClose={reset}
      title={
        deployed
          ? editing
            ? "Agent updated"
            : "Agent deployed"
          : editing
            ? `Edit ${edit?.name}`
            : "Deploy a git-agent"
      }
      size="lg"
    >
      <div className="grid gap-density-4 p-density-4">
        {deployed ? (
          <DeployedSummary result={deployed} />
        ) : (
          <>
            <TargetPicker value={target} onChange={setTarget} disabled={editing} />
            <PreflightNotice
              loading={preflight.isLoading}
              error={preflight.error}
              preflight={preflight.data}
              onRetry={() => void preflight.refetch()}
            />

            {ready && (
              <DeployForm
                form={form}
                preflight={preflight.data}
                blockers={blockers}
                namespaces={knownNamespaces}
                willCreateNamespace={willCreateNamespace}
                identityLocked={editing}
                onChange={set}
              />
            )}

            {preview && <PreviewPlan result={preview} />}

            {deploy.error && (
              <p role="alert" className="text-xs text-destructive">
                {deploy.error instanceof Error
                  ? deploy.error.message
                  : String(deploy.error)}
              </p>
            )}

            {/* Beside the buttons it disables, because the fields it names sit
                far up a long form and `disabled:pointer-events-none` means the
                button itself can never explain anything on hover. */}
            {ready && <FormErrorSummary errors={blockerSummary(blockers)} />}

            <div className="flex justify-end gap-density-2">
              <Button variant="ghost" onClick={reset}>
                Cancel
              </Button>
              <Button
                variant="secondary"
                disabled={!canSubmit || deploy.isPending}
                onClick={() => deploy.mutate(true)}
              >
                {deploy.isPending && !deployed
                  ? "Checking…"
                  : editing
                    ? "Preview update"
                    : "Preview"}
              </Button>
              <Button
                disabled={!canSubmit || deploy.isPending}
                onClick={() => deploy.mutate(false)}
              >
                {deploy.isPending
                  ? editing
                    ? "Updating…"
                    : "Deploying…"
                  : editing
                    ? "Update"
                    : "Deploy"}
              </Button>
            </div>
          </>
        )}

        {deployed && (
          <div className="flex justify-end">
            <Button onClick={reset}>Done</Button>
          </div>
        )}
      </div>
    </Modal>
  );
}

function initialRequest(
  edit: { name: string; deployment: GitAgentDeployment } | undefined,
): DeployRequest {
  if (!edit) return { name: "", target: "docker" };
  if (!edit.deployment.config) {
    throw new Error(`deployment ${edit.name} has no saved configuration`);
  }
  return {
    ...edit.deployment.config,
    name: edit.name,
    target: edit.deployment.target,
  };
}

function TargetPicker({
  value,
  onChange,
  disabled = false,
}: {
  value: DeployTarget;
  onChange: (target: DeployTarget) => void;
  disabled?: boolean;
}) {
  return (
    <fieldset className="grid gap-density-2">
      <legend className="text-xs text-muted-foreground">Where it runs</legend>
      <div className="grid grid-cols-2 gap-density-2">
        {TARGETS.map((option) => (
          <button
            key={option.value}
            type="button"
            aria-label={option.label}
            aria-pressed={value === option.value}
            disabled={disabled}
            onClick={() => onChange(option.value)}
            className={`rounded-md border p-density-3 text-left text-xs ${
              value === option.value
                ? "border-primary bg-primary/5"
                : "border-border hover:bg-muted"
            }`}
          >
            <span className="font-medium">{option.label}</span>
            <p className="text-muted-foreground">{option.blurb}</p>
          </button>
        ))}
      </div>
    </fieldset>
  );
}

/**
 * The preflight result, stated before the form rather than after a failed
 * submit. A refusal here is the same one the CLI gives, and usually names the
 * command that fixes it.
 */
function PreflightNotice({
  loading,
  error,
  preflight,
  onRetry,
}: {
  loading: boolean;
  error: unknown;
  preflight: DeployPreflight | undefined;
  onRetry: () => void;
}) {
  if (loading) {
    return (
      <p className="text-xs text-muted-foreground">Checking this target…</p>
    );
  }
  if (error) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {error instanceof Error ? error.message : String(error)}
      </p>
    );
  }
  if (!preflight) return null;

  if (!preflight.ready) {
    return (
      <div
        role="alert"
        className="grid gap-density-2 rounded-md border border-destructive/40 bg-destructive/5 p-density-3"
      >
        <p className="text-xs font-medium text-destructive">
          Cannot deploy to {preflight.target} from this host
        </p>
        <p className="whitespace-pre-wrap text-xs text-muted-foreground">
          {preflight.reason}
        </p>
        <div>
          <Button size="sm" variant="secondary" onClick={onRetry}>
            Re-check
          </Button>
        </div>
      </div>
    );
  }

  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 rounded-md border border-border bg-muted/40 p-density-3 text-xs">
      {preflight.runtime && (
        <>
          <dt className="text-muted-foreground">Runtime</dt>
          <dd>{preflight.runtime}</dd>
        </>
      )}
      <dt className="text-muted-foreground">Mailbox</dt>
      <dd className="font-mono break-all">
        {preflight.mailboxListen}
        {/* Which process is the supervisor: https means `captain serve` hosts
            it, ssh means a separate `git-agent serve --role mailbox`. */}
        {preflight.transport && (
          <span className="ml-density-2 font-sans text-muted-foreground">
            over {preflight.transport}
          </span>
        )}
      </dd>
      {preflight.supervisor && (
        <>
          <dt className="text-muted-foreground">Agent reaches it at</dt>
          <dd className="font-mono break-all">
            {preflight.supervisor}{" "}
            <span className="text-muted-foreground">
              ({preflight.supervisorFrom})
            </span>
          </dd>
        </>
      )}
    </dl>
  );
}
