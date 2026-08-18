import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Combobox,
  Field,
  InputField,
  NamespacePicker,
  Switch,
} from "@flanksource/clicky-ui/components";

import { RoutingSection } from "./GitAgentDeployRouting";
import type { DeployBlockers } from "./gitAgentDeployValidation";
import {
  fetchSecrets,
  type DeployPreflight,
  type DeployRequest,
} from "./sandboxData";

export type DeployFieldSetter = <K extends keyof DeployRequest>(
  key: K,
  value: DeployRequest[K],
) => void;

export function DeployForm({
  form,
  preflight,
  blockers,
  namespaces,
  willCreateNamespace,
  identityLocked = false,
  onChange,
}: {
  form: DeployRequest;
  preflight: DeployPreflight | undefined;
  blockers: DeployBlockers;
  namespaces: string[];
  willCreateNamespace: boolean;
  identityLocked?: boolean;
  onChange: DeployFieldSetter;
}) {
  const [advanced, setAdvanced] = useState(false);
  const kubernetes = form.target === "kubernetes";
  // Only once the advanced block is open: until then nothing renders the list,
  // and a cluster call per modal open would be paid by every docker deploy too.
  const namespace = (form.namespace ?? "").trim() || (preflight?.namespace ?? "");
  const secrets = useQuery({
    queryKey: ["git-agent-secrets", namespace, form.kubeContext],
    queryFn: () => fetchSecrets({ namespace, kubeContext: form.kubeContext }),
    enabled: advanced && kubernetes,
    retry: false,
  });
  const namespaceSecrets = useMemo(
    () => (secrets.data ?? []).map((name) => ({ value: name, label: name })),
    [secrets.data],
  );
  // NamespacePicker loads once per getter identity, so the getter is memoized on
  // the list the modal already fetched rather than issuing a second request.
  const loadNamespaces = useCallback(() => Promise.resolve(namespaces), [namespaces]);

  return (
    <div className="grid gap-density-3">
      <Field label="Agent name" htmlFor="deploy-name" required error={blockers.name}>
        <InputField
          id="deploy-name"
          value={form.name}
          onChange={(value: string) => onChange("name", value)}
          placeholder="worker-01"
          invalid={Boolean(blockers.name)}
          disabled={identityLocked}
          autoFocus
        />
      </Field>

      {preflight?.supervisorRequired && (
        <SupervisorField
          form={form}
          preflight={preflight}
          blocker={blockers.supervisorAddress}
          onChange={onChange}
        />
      )}

      {kubernetes && (
        <Field
          label="Namespace"
          helper={
            identityLocked
              ? "The namespace is part of this deployment's identity and cannot be changed during an update."
              : willCreateNamespace
              ? `${form.namespace} does not exist in this cluster and will be created. It is the one ` +
                `cluster-scoped change here, and undeploy does not remove it.`
              : `Select one, or type a name to create it. Empty uses the kubeconfig context's ` +
                `(${preflight?.namespace ?? "unknown"}).`
          }
        >
          {/*
            Not `strict`: a name absent from the cluster is the create half of
            "select or create", so flagging it invalid would mark the intended
            action as an error. The hint says which of the two is happening.
          */}
          {identityLocked ? (
            <InputField value={form.namespace ?? ""} disabled />
          ) : (
            <NamespacePicker
              value={form.namespace ?? ""}
              onChange={(value: string) => onChange("namespace", value)}
              loadNamespaces={loadNamespaces}
              placeholder={preflight?.namespace ?? "default"}
            />
          )}
        </Field>
      )}

      {kubernetes && (
        <RoutingSection
          form={form}
          preflight={preflight}
          blockers={blockers}
          onChange={onChange}
        />
      )}

      <Field
        label="Model credentials"
        htmlFor="deploy-env"
        helper={
          "Environment variable NAMES to forward from this process — values are read here, never sent. " +
          "Without one the agent enrolls, goes ready, and fails its first task."
        }
      >
        <InputField
          id="deploy-env"
          value={(form.env ?? []).join(", ")}
          onChange={(value: string) => onChange("env", splitList(value))}
          placeholder="ANTHROPIC_API_KEY, OPENAI_API_KEY"
        />
      </Field>

      <button
        type="button"
        className="justify-self-start text-xs text-muted-foreground underline"
        onClick={() => setAdvanced((shown) => !shown)}
      >
        {advanced ? "Hide" : "Show"} image and sizing
      </button>

      {advanced && (
        <div className="grid gap-density-3 rounded-md border border-border p-density-3">
          <Field label="Image">
            <InputField
              value={form.image ?? ""}
              onChange={(value: string) => onChange("image", value)}
              placeholder="ghcr.io/flanksource/captain:latest"
            />
          </Field>
          <div className="grid grid-cols-3 gap-density-2">
            <Field label="CPU limit">
              <InputField
                value={form.cpuLimit ?? ""}
                onChange={(value: string) => onChange("cpuLimit", value)}
                placeholder="2"
              />
            </Field>
            <Field label="Memory limit">
              <InputField
                value={form.memoryLimit ?? ""}
                onChange={(value: string) => onChange("memoryLimit", value)}
                placeholder="4Gi"
              />
            </Field>
            <Field label="Storage">
              <InputField
                value={form.storage ?? ""}
                onChange={(value: string) => onChange("storage", value)}
                placeholder="20Gi"
              />
            </Field>
          </div>
          {kubernetes && (
            <>
              <Field
                label="Secrets exposed via envFrom"
                helper="Names of existing Secrets in the target namespace."
              >
                <InputField
                  value={(form.envFromSecret ?? []).join(", ")}
                  onChange={(value: string) =>
                    onChange("envFromSecret", splitList(value))
                  }
                  placeholder="captain-model-keys"
                />
              </Field>
              <Field
                label="Agent login Secret"
                helper="Secret of redacted claude/codex logins kept fresh by `captain sandbox credentials`. Mounted read-only at /run/captain/credentials."
              >
                {/* Every Secret, not just TLS: this one is written by the
                    credential sync and has no distinguishing type. */}
                <Combobox
                  options={namespaceSecrets}
                  value={form.credentialsSecret ?? ""}
                  onChange={(value: string) =>
                    onChange("credentialsSecret", value)
                  }
                  ariaLabel="Agent login Secret"
                  allowCustomValue
                  loading={secrets.isFetching}
                  placeholder="captain-agent-credentials"
                />
              </Field>
            </>
          )}
        </div>
      )}

      {!identityLocked && (
        <div className="text-xs text-muted-foreground">
          <Switch
            checked={form.replace ?? false}
            onChange={(checked: boolean) => onChange("replace", checked)}
            label="Replace an existing deployment of this name"
          />
        </div>
      )}
    </div>
  );
}

/**
 * The address the deployed agent dials back on.
 *
 * A picker rather than a box because the addresses worth trying are facts about
 * this host that the preflight already enumerated — but not a closed set: none
 * of them is *proven* routable from a managed cluster, and the address that
 * works is often a name or a NAT that this host cannot see at all.
 */
function SupervisorField({
  form,
  preflight,
  blocker,
  onChange,
}: {
  form: DeployRequest;
  preflight: DeployPreflight;
  blocker: string | undefined;
  onChange: DeployFieldSetter;
}) {
  const options = useMemo(
    () =>
      (preflight.supervisorCandidates ?? []).map((address) => ({
        value: address,
        label: address,
      })),
    [preflight.supervisorCandidates],
  );

  return (
    <Field
      label="Address the agent reaches this supervisor on"
      required
      error={blocker}
    >
      <Combobox
        options={options}
        value={form.supervisorAddress ?? ""}
        onChange={(value: string) => onChange("supervisorAddress", value)}
        ariaLabel="Supervisor address"
        // The offers are ranked guesses, not a closed set, so a typed address is
        // the expected path on any cluster reached through a name or a NAT.
        allowCustomValue
        invalid={Boolean(blocker)}
        // The scheme has to match the mailbox that answered: an https
        // supervisor address against an ssh mailbox reaches nothing.
        placeholder={
          preflight.transport === "https"
            ? "https://captain.example.internal:9020"
            : "ssh://captain.example.internal:7422"
        }
      />
    </Field>
  );
}

/** Splits a comma or whitespace separated list, dropping blanks. */
export function splitList(value: string): string[] {
  return value
    .split(/[,\s]+/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/**
 * Splits `key=value` entries on commas and newlines only.
 *
 * Not splitList: an annotation value legitimately contains spaces — a
 * source-range allowlist or a snippet — and splitting on whitespace would turn
 * one annotation into several malformed ones.
 */
export function splitAnnotations(value: string): string[] {
  return value
    .split(/[\n,]+/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}
