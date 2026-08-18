import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Combobox, Field, InputField } from "@flanksource/clicky-ui/components";

import { splitAnnotations, type DeployFieldSetter } from "./GitAgentDeployForm";
import { externalHost, type DeployBlockers } from "./gitAgentDeployValidation";
import {
  fetchClusterIssuers,
  fetchSecrets,
  TLS_SECRET_TYPE,
  type DeployPreflight,
  type DeployRequest,
} from "./sandboxData";

/**
 * What a controller other than ingress-nginx needs to serve a git push.
 *
 * captain refuses a non-nginx class with no annotations because its own
 * defaults are written in nginx's vocabulary, and it cannot check another
 * controller's spelling. That refusal is right, but on a cluster whose only
 * IngressClass is traefik it is also a dead end — so where the translation is
 * known, it is applied on selection rather than offered.
 *
 * traefik: the pod terminates its own TLS, so the hop from the controller has
 * to be re-encrypted — serversscheme is its equivalent of nginx's
 * backend-protocol: HTTPS. The rest of captain's nginx defaults have no
 * counterpart and need none: traefik neither buffers nor caps request bodies,
 * and its read/write timeouts are entryPoint-level (respondingTimeouts), which
 * an Ingress annotation cannot reach at all.
 */
const CONTROLLER_EQUIVALENTS: Record<string, string[]> = {
  traefik: ["traefik.ingress.kubernetes.io/service.serversscheme=https"],
};

/**
 * How a supervisor outside the cluster reaches the agent.
 *
 * This is the one input of a Kubernetes deploy that cannot be detected: captain
 * cannot prove a route it did not create, and the name that would work does not
 * exist until someone adds a DNS record. So it sits in the form proper rather
 * than behind an advanced toggle — without it the deploy is refused, and with a
 * wrong value the agent enrolls, looks healthy, and never receives a dispatch.
 */
export function RoutingSection({
  form,
  preflight,
  blockers,
  onChange,
}: {
  form: DeployRequest;
  preflight: DeployPreflight | undefined;
  blockers: DeployBlockers;
  onChange: DeployFieldSetter;
}) {
  const classes = useMemo(
    () =>
      (preflight?.ingressClasses ?? []).map((name) => ({ value: name, label: name })),
    [preflight?.ingressClasses],
  );
  const host = externalHost(form);
  const ingressClass = (form.ingressClass ?? "").trim();
  const equivalents = CONTROLLER_EQUIVALENTS[ingressClass];

  // Applied on selection rather than offered: where captain knows a
  // controller's translation there is nothing for the operator to decide, and
  // the alternative is a refusal whose only remedy is retyping what we already
  // know. Keyed on the class alone, so an operator who then edits or clears the
  // annotations is not fought by the effect re-adding them.
  useEffect(() => {
    if (!equivalents) return;
    const current = form.ingressAnnotation ?? [];
    const missing = equivalents.filter((entry) => !current.includes(entry));
    if (missing.length > 0) onChange("ingressAnnotation", [...current, ...missing]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ingressClass]);
  // Held here rather than derived from which field is filled, so choosing a
  // source before typing into it does not immediately flip back. cert-manager's
  // absence decides the default, because an issuer would be inert without it.
  const [source, setSource] = useState<CertificateChoice>(() =>
    preflight?.certManagerInstalled ? "issuer" : "secret",
  );

  return (
    <fieldset className="grid gap-density-3 rounded-md border border-border p-density-3">
      <legend className="px-1 text-xs text-muted-foreground">Routing</legend>

      {preflight?.inCluster && (
        <p className="text-xs text-muted-foreground">
          captain is running in this cluster, so the agent is reachable at its Service
          address. An Ingress is only needed to reach it from outside.
        </p>
      )}

      <Field
        label="Domain"
        htmlFor="deploy-domain"
        required={preflight?.domainRequired ?? false}
        error={blockers.domain}
        helper={
          host
            ? `Published at ${host}. captain does NOT create the DNS record — that name must ` +
              `already resolve to the ingress controller, or the certificate is never issued.`
            : "The agent is published at <name>.<domain> behind an Ingress."
        }
      >
        <InputField
          id="deploy-domain"
          value={form.domain ?? ""}
          onChange={(value: string) => onChange("domain", value)}
          placeholder="agents.example.com"
          invalid={Boolean(blockers.domain)}
        />
      </Field>

      <Field
        label="Ingress class"
        helper="The controller serving this domain. Empty uses nginx."
      >
        {/*
          Deliberately never pre-filled with "nginx": the server applies that
          default from the flag's own tag, so a blank field cannot drift from it
          — and a form that sends a value it was not given makes every deploy
          look configured.
        */}
        <Combobox
          options={classes}
          value={form.ingressClass ?? ""}
          onChange={(value: string) => onChange("ingressClass", value)}
          ariaLabel="Ingress class"
          allowCustomValue
          placeholder="nginx"
        />
      </Field>

      <CertificateSource
        form={form}
        preflight={preflight}
        blockers={blockers}
        source={source}
        // Clearing the other field on the way out is what keeps the mutually
        // exclusive pair from being sent as a pair the server has to refuse.
        onSource={(chosen) => {
          setSource(chosen);
          onChange(chosen === "issuer" ? "ingressTlsSecret" : "ingressIssuer", undefined);
        }}
        onChange={onChange}
      />

      {/* The blocker belongs here, not on Ingress class: the class is already
          what the operator wanted, and annotations are the field that fixes it. */}
      <Field
        label="Ingress annotations"
        htmlFor="deploy-annotations"
        required={Boolean(blockers.ingressAnnotation)}
        error={blockers.ingressAnnotation}
        helper="key=value, one per line or comma separated. Merged over the ingress-nginx defaults."
      >
        <InputField
          id="deploy-annotations"
          value={(form.ingressAnnotation ?? []).join(", ")}
          onChange={(value: string) =>
            onChange("ingressAnnotation", splitAnnotations(value))
          }
          // Follows the chosen class: offering an nginx example beside a traefik
          // class is guidance that cannot work.
          placeholder={
            equivalents
              ? equivalents.join(", ")
              : "nginx.ingress.kubernetes.io/whitelist-source-range=10.0.0.0/8"
          }
          invalid={Boolean(blockers.ingressAnnotation)}
        />
      </Field>

      <Field
        label="Advertise URL"
        htmlFor="deploy-advertise"
        required={Boolean(blockers.advertise)}
        error={blockers.advertise}
        helper="Optional: a route you manage yourself, instead of an Ingress captain creates."
      >
        <InputField
          id="deploy-advertise"
          value={form.advertise ?? ""}
          onChange={(value: string) => onChange("advertise", value)}
          placeholder="https://worker-01.agents.example.com"
          invalid={Boolean(blockers.advertise)}
        />
      </Field>
    </fieldset>
  );
}

type CertificateChoice = "issuer" | "secret";

/**
 * Where the certificate for the agent's host comes from.
 *
 * A choice rather than two fields because the server refuses both at once, and
 * refuses neither: without a certificate the controller answers for that host
 * with its own, and the supervisor's push fails verification.
 */
function CertificateSource({
  form,
  preflight,
  blockers,
  source,
  onSource,
  onChange,
}: {
  form: DeployRequest;
  preflight: DeployPreflight | undefined;
  blockers: DeployBlockers;
  source: CertificateChoice;
  onSource: (chosen: CertificateChoice) => void;
  onChange: DeployFieldSetter;
}) {
  // An issuer annotation is inert without the controller that acts on it, so the
  // option is closed rather than left to fail after the token is minted.
  const certManager = preflight?.certManagerInstalled ?? false;
  const blocker = blockers.ingressIssuer ?? blockers.ingressTlsSecret;
  const usingSecret = source === "secret";
  // A certificate is only mandatory once there is a host to certify.
  const hasDomain = Boolean((form.domain ?? "").trim());

  // Scoped to the namespace the deploy targets, and re-read when it changes:
  // the same Secret name means a different object in another namespace.
  const namespace = (form.namespace ?? "").trim() || (preflight?.namespace ?? "");
  const secrets = useQuery({
    queryKey: ["git-agent-tls-secrets", namespace, form.kubeContext],
    queryFn: () =>
      fetchSecrets({
        namespace,
        kubeContext: form.kubeContext,
        type: TLS_SECRET_TYPE,
      }),
    enabled: usingSecret,
    retry: false,
  });
  const clusterIssuers = useQuery({
    queryKey: ["git-agent-cluster-issuers", form.kubeContext],
    queryFn: () => fetchClusterIssuers(form.kubeContext),
    // An issuer is inert without the controller, so the list is only worth
    // fetching where one exists.
    enabled: !usingSecret && certManager,
    retry: false,
  });
  const tlsSecrets = useMemo(
    () => (secrets.data ?? []).map((name) => ({ value: name, label: name })),
    [secrets.data],
  );
  const issuers = useMemo(
    () => (clusterIssuers.data ?? []).map((name) => ({ value: name, label: name })),
    [clusterIssuers.data],
  );

  return (
    <div className="grid gap-density-2">
      <div className="flex gap-density-3 text-xs text-muted-foreground">
        <label className="flex items-center gap-1">
          <input
            type="radio"
            name="certificate-source"
            checked={!usingSecret}
            disabled={!certManager}
            onChange={() => onSource("issuer")}
          />
          <span>cert-manager issuer</span>
        </label>
        <label className="flex items-center gap-1">
          <input
            type="radio"
            name="certificate-source"
            checked={usingSecret}
            onChange={() => onSource("secret")}
          />
          <span>existing TLS Secret</span>
        </label>
      </div>

      {!certManager && (
        <p className="text-xs text-muted-foreground/80">
          cert-manager is not installed in this cluster, so an issuer would be inert.
          Name a Secret that already covers the agent&apos;s host.
        </p>
      )}

      {usingSecret ? (
        <Field label="TLS Secret" required={hasDomain} error={blocker}>
          {/*
            A picker, because a Secret that is not kubernetes.io/tls cannot
            serve the agent's host and the Ingress would be created pointing at
            it regardless. Still allowCustomValue: a namespace this cannot list
            is not proof the name is wrong.
          */}
          <Combobox
            options={tlsSecrets}
            value={form.ingressTlsSecret ?? ""}
            onChange={(value: string) => onChange("ingressTlsSecret", value)}
            ariaLabel="TLS Secret"
            allowCustomValue
            loading={secrets.isFetching}
            invalid={Boolean(blocker)}
            placeholder="agents-example-com-wildcard"
          />
        </Field>
      ) : (
        <Field label="ClusterIssuer" required={hasDomain} error={blocker}>
          <Combobox
            options={issuers}
            value={form.ingressIssuer ?? ""}
            onChange={(value: string) => onChange("ingressIssuer", value)}
            ariaLabel="ClusterIssuer"
            allowCustomValue
            loading={clusterIssuers.isFetching}
            disabled={!certManager}
            invalid={Boolean(blocker)}
            placeholder="letsencrypt-prod"
          />
        </Field>
      )}
    </div>
  );
}
