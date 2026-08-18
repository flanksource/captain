import type { JsonSchemaFormError } from "@flanksource/clicky-ui/components";

import type { DeployPreflight, DeployRequest } from "./sandboxData";

/**
 * What the deploy form refuses, and why — the same refusals the server would
 * give, stated before submit rather than after.
 *
 * Pure so every rule is testable without rendering a modal, and so the modal's
 * "can I submit" is one expression rather than a growing chain of booleans. Each
 * rule below names the server check it mirrors; a rule with no counterpart there
 * would be the form inventing policy.
 */
export type DeployBlockers = Partial<Record<keyof DeployRequest, string>>;

/** The CLI's own default, applied server-side from the `default:` flag tag. */
const DEFAULT_INGRESS_CLASS = "nginx";

const trimmed = (value: string | undefined) => (value ?? "").trim();

export function deployBlockers(
  form: DeployRequest,
  preflight: DeployPreflight | undefined,
): DeployBlockers {
  const blockers: DeployBlockers = {};
  if (!trimmed(form.name)) blockers.name = "An agent name is required.";
  // Nothing else can be judged against a target that has not answered, and the
  // PreflightNotice is already saying why.
  if (!preflight?.ready) return blockers;

  if (preflight.supervisorRequired && !trimmed(form.supervisorAddress)) {
    blockers.supervisorAddress =
      "Required: captain is not running in the target cluster, so no route back to this host " +
      `can be proven. This host's mailbox listens on ${preflight.mailboxListen ?? "its recorded address"}, ` +
      "which a managed cluster usually cannot reach.";
  }

  Object.assign(blockers, routeBlockers(form, preflight));
  return blockers;
}

/**
 * The external route, which is the half of a kubernetes deploy nothing can
 * detect: a supervisor outside the cluster cannot dial a ClusterIP, and the name
 * that would work does not exist until someone creates a DNS record.
 */
function routeBlockers(form: DeployRequest, preflight: DeployPreflight): DeployBlockers {
  if (form.target !== "kubernetes") return {};
  const blockers: DeployBlockers = {};
  const domain = trimmed(form.domain);
  const issuer = trimmed(form.ingressIssuer);
  const secret = trimmed(form.ingressTlsSecret);

  // resolveAdvertiseAddress refuses with neither, whichever transport answered.
  // Which field carries the message is what the transport decides: an Ingress is
  // an HTTP router, so it can only front a mailbox already speaking https.
  if (preflight.domainRequired && !domain && !trimmed(form.advertise)) {
    const missing =
      preflight.transport === "https"
        ? ("domain" as const)
        : ("advertise" as const);
    blockers[missing] =
      preflight.transport === "https"
        ? "Required: captain is not running in the target cluster, so a ClusterIP is the only thing " +
          "left to advertise and the agent would never receive a dispatch. Publish it behind an " +
          "Ingress, or give an advertise URL for a route you manage yourself."
        : "Required: the mailbox answered over ssh, which an Ingress cannot front. Give the address " +
          "of a route you manage yourself — a LoadBalancer or NodePort the supervisor can dial.";
  }
  if (!domain) return blockers;

  // Mirrors applyExternalRoute: a host with no certificate means the controller
  // answers for it with its own, and the supervisor's push fails verification.
  if (issuer && secret) {
    blockers.ingressTlsSecret =
      "A cert-manager issuer and an existing TLS Secret are mutually exclusive; keep one.";
  } else if (!issuer && !secret) {
    blockers.ingressIssuer =
      `${domain} needs a certificate for the agent's host: a cert-manager ClusterIssuer, or an ` +
      "existing TLS Secret. Without either, the controller answers for that host with its own " +
      "default certificate and the supervisor's push fails verification.";
  }

  // An acknowledgement gate, not a validation: captain cannot check another
  // controller's spelling, so any annotation satisfies it.
  const ingressClass = trimmed(form.ingressClass);
  if (
    ingressClass &&
    ingressClass !== DEFAULT_INGRESS_CLASS &&
    (form.ingressAnnotation ?? []).length === 0
  ) {
    blockers.ingressAnnotation =
      `${ingressClass} is not ingress-nginx, so the buffering, body-size and timeout settings a git ` +
      "push depends on are not set. Add that controller's equivalents as annotations.";
  }
  return blockers;
}

/**
 * What each blocker is called where the operator has to fix it.
 *
 * The form is long enough that a blocking field can sit off-screen from the
 * disabled button, so the summary has to name the field rather than only state
 * the problem.
 */
const FIELD_LABELS: Partial<Record<keyof DeployRequest, string>> = {
  name: "Agent name",
  supervisorAddress: "Supervisor address",
  domain: "Domain",
  ingressIssuer: "ClusterIssuer",
  ingressTlsSecret: "TLS Secret",
  ingressAnnotation: "Ingress annotations",
  advertise: "Advertise URL",
};

/**
 * The blockers as clicky-ui's FormErrorSummary consumes them.
 *
 * `instancePath` is that component's display-prefix slot — it renders
 * `${instancePath}: ${message}` — so the field's human label goes there rather
 * than a JSON pointer no operator would recognise.
 */
export function blockerSummary(blockers: DeployBlockers): JsonSchemaFormError[] {
  return Object.entries(blockers).map(([key, message]) => ({
    instancePath: FIELD_LABELS[key as keyof DeployRequest] ?? key,
    message: message as string,
  }));
}

/** The host the Ingress would publish, the same join resolveExternalHost makes. */
export function externalHost(form: DeployRequest): string {
  const domain = trimmed(form.domain).replace(/^\.+|\.+$/g, "");
  const name = trimmed(form.name);
  return domain && name ? `${name}.${domain}` : "";
}
