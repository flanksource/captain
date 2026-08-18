import { describe, expect, it } from "vitest";

import {
  blockerSummary,
  deployBlockers,
  externalHost,
} from "./gitAgentDeployValidation";
import type { DeployPreflight, DeployRequest } from "./sandboxData";

/** A cluster captain is not running in — the topology that needs a route. */
const OUTSIDE_CLUSTER: DeployPreflight = {
  target: "kubernetes",
  ready: true,
  supervisorRequired: true,
  supervisorCandidates: ["https://192.168.1.20:9020"],
  mailboxListen: "0.0.0.0:9020",
  transport: "https",
  namespace: "captain",
  inCluster: false,
  domainRequired: true,
  ingressClasses: ["nginx", "traefik"],
  certManagerInstalled: true,
};

const DOCKER_READY: DeployPreflight = {
  target: "docker",
  ready: true,
  supervisorRequired: false,
  transport: "ssh",
  inCluster: false,
  domainRequired: false,
  certManagerInstalled: false,
};

/** A deploy with everything the outside-cluster topology demands. */
function completeKubernetesForm(overrides: Partial<DeployRequest> = {}): DeployRequest {
  return {
    name: "worker-01",
    target: "kubernetes",
    supervisorAddress: "https://192.168.1.20:9020",
    domain: "agents.example.com",
    ingressIssuer: "letsencrypt-prod",
    ...overrides,
  };
}

describe("deployBlockers", () => {
  it("passes a form that supplies every address the topology cannot detect", () => {
    expect(deployBlockers(completeKubernetesForm(), OUTSIDE_CLUSTER)).toEqual({});
  });

  it("requires an agent name", () => {
    const blockers = deployBlockers(completeKubernetesForm({ name: "  " }), OUTSIDE_CLUSTER);
    expect(blockers.name).toBeDefined();
  });

  // Nothing downstream can be judged against a target that refused, and the
  // preflight notice is already saying why — so the form must not pile on.
  it("judges nothing but the name while the target is not deployable", () => {
    const refused: DeployPreflight = { ...OUTSIDE_CLUSTER, ready: false, reason: "no mailbox" };
    expect(deployBlockers({ name: "", target: "kubernetes" }, refused)).toEqual({
      name: expect.any(String),
    });
  });

  it("requires the supervisor address the preflight could not resolve", () => {
    const blockers = deployBlockers(
      completeKubernetesForm({ supervisorAddress: "" }),
      OUTSIDE_CLUSTER,
    );
    expect(blockers.supervisorAddress).toContain("no route back to this host can be proven");
  });

  // An Ingress is an HTTP router, so which field is mandatory follows the
  // transport the mailbox answered on rather than being fixed.
  it("demands a domain over https and an advertise URL over ssh", () => {
    const bare = completeKubernetesForm({ domain: undefined, ingressIssuer: undefined });

    const https = deployBlockers(bare, OUTSIDE_CLUSTER);
    expect(https.domain).toContain("Ingress");
    expect(https.advertise).toBeUndefined();

    const ssh = deployBlockers(
      { ...bare, supervisorAddress: "ssh://192.168.1.20:7422" },
      { ...OUTSIDE_CLUSTER, transport: "ssh" },
    );
    expect(ssh.advertise).toContain("an Ingress cannot front");
    expect(ssh.domain).toBeUndefined();
  });

  it("accepts an advertise URL in place of a domain, on either transport", () => {
    const advertised = completeKubernetesForm({
      domain: undefined,
      ingressIssuer: undefined,
      advertise: "https://worker-01.agents.example.com",
    });
    expect(deployBlockers(advertised, OUTSIDE_CLUSTER)).toEqual({});
    expect(
      deployBlockers(advertised, { ...OUTSIDE_CLUSTER, transport: "ssh" }),
    ).toEqual({});
  });

  // In-cluster is the other supported topology, not a degraded one: the agent is
  // reachable at its Service address, so no route is required.
  it("requires no route when captain is itself in the cluster", () => {
    const inCluster = { ...OUTSIDE_CLUSTER, inCluster: true, domainRequired: false };
    const bare = completeKubernetesForm({ domain: undefined, ingressIssuer: undefined });
    expect(deployBlockers(bare, inCluster)).toEqual({});
  });

  // Without a certificate the controller answers for the host with its own, and
  // the supervisor's push fails verification after everything is created.
  it("requires exactly one certificate source once a domain is set", () => {
    const neither = deployBlockers(
      completeKubernetesForm({ ingressIssuer: undefined }),
      OUTSIDE_CLUSTER,
    );
    expect(neither.ingressIssuer).toContain("needs a certificate");

    const both = deployBlockers(
      completeKubernetesForm({ ingressTlsSecret: "wildcard" }),
      OUTSIDE_CLUSTER,
    );
    expect(both.ingressTlsSecret).toContain("mutually exclusive");

    const secretOnly = completeKubernetesForm({
      ingressIssuer: undefined,
      ingressTlsSecret: "wildcard",
    });
    expect(deployBlockers(secretOnly, OUTSIDE_CLUSTER)).toEqual({});
  });

  // captain cannot check another controller's spelling, so any annotation
  // satisfies the gate — it is an acknowledgement, not a validation.
  it("makes a non-nginx class acknowledge its own equivalents", () => {
    const traefik = completeKubernetesForm({ ingressClass: "traefik" });
    expect(deployBlockers(traefik, OUTSIDE_CLUSTER).ingressAnnotation).toContain("traefik");

    const acknowledged = { ...traefik, ingressAnnotation: ["traefik.ingress.kubernetes.io/x=y"] };
    expect(deployBlockers(acknowledged, OUTSIDE_CLUSTER)).toEqual({});
  });

  // Blank keeps the server's own "nginx" default, so it must not trip the gate
  // that only applies to a class captain has no settings for.
  it("treats a blank class as nginx", () => {
    const blank = completeKubernetesForm({ ingressClass: "" });
    expect(deployBlockers(blank, OUTSIDE_CLUSTER)).toEqual({});
  });

  // A docker sidecar is reached on its published loopback port; the whole route
  // question does not arise, and the server refuses the flags outright.
  it("asks nothing about routing on docker", () => {
    expect(deployBlockers({ name: "worker-01", target: "docker" }, DOCKER_READY)).toEqual({});
  });
});

describe("blockerSummary", () => {
  // The summary sits beside the submit button, often screens away from the
  // field at fault, so a blocker that did not name its field would leave the
  // operator hunting the same way an unexplained disabled button does.
  it("names the field an operator has to go and fix", () => {
    const blockers = deployBlockers(
      completeKubernetesForm({ ingressIssuer: undefined }),
      OUTSIDE_CLUSTER,
    );

    expect(blockerSummary(blockers)).toEqual([
      {
        instancePath: "ClusterIssuer",
        message: expect.stringContaining("needs a certificate"),
      },
    ]);
  });

  it("is empty when nothing blocks, so the summary can render nothing", () => {
    expect(blockerSummary(deployBlockers(completeKubernetesForm(), OUTSIDE_CLUSTER))).toEqual(
      [],
    );
  });
});

describe("externalHost", () => {
  it("joins the agent name to the domain the Ingress serves", () => {
    expect(externalHost({ name: "worker-01", target: "kubernetes", domain: "agents.example.com" }))
      .toBe("worker-01.agents.example.com");
  });

  // A pasted domain routinely carries a trailing dot or a leading separator, and
  // "worker-01..example.com" is a name that can never resolve.
  it("tolerates a domain typed with stray separators", () => {
    expect(externalHost({ name: "worker-01", target: "kubernetes", domain: ".example.com." }))
      .toBe("worker-01.example.com");
  });

  it("has no host until both halves are given", () => {
    expect(externalHost({ name: "worker-01", target: "kubernetes" })).toBe("");
    expect(externalHost({ name: "", target: "kubernetes", domain: "example.com" })).toBe("");
  });
});
