import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Combobox, Field, InputField, Panel } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";

import {
  fetchCredentials,
  saveCredentialsConfig,
  syncCredentials,
  type CredentialDestination,
  type CredentialStatus,
  type CredentialsConfig,
  type CredentialsSyncResult,
} from "./sandboxData";

/**
 * The agent-login mirror: which of this host's model-provider logins get copied
 * where, and how long they stay valid.
 *
 * A deployed sidecar with no credential enrolls, reports ready, and fails its
 * first task minutes later — the same late failure the deploy preflight exists
 * to prevent — so expiry and destinations are stated rather than left to a CLI
 * an operator has to remember to run.
 */
export function SandboxCredentials() {
  const client = useQueryClient();
  const credentials = useQuery({
    queryKey: ["sandbox-credentials"],
    queryFn: fetchCredentials,
    retry: false,
  });

  // Held locally so a half-edited destination is not written on every keystroke,
  // and reseeded whenever the server's copy changes under us.
  const [draft, setDraft] = useState<CredentialsConfig | undefined>();
  useEffect(() => {
    if (credentials.data) setDraft(credentials.data.config);
  }, [credentials.data]);

  const save = useMutation({
    mutationFn: (config: CredentialsConfig) => saveCredentialsConfig(config),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sandbox-credentials"] }),
  });
  const sync = useMutation({
    // No override: the button publishes to what is configured, which is what
    // the supervisor's own loop would do on its next tick.
    mutationFn: () => syncCredentials(),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sandbox-credentials"] }),
  });

  const error = credentials.error ?? save.error ?? sync.error;

  return (
    <Panel
      title="Agent logins"
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={sync.isPending || !credentials.data}
          onClick={() => sync.mutate()}
        >
          {sync.isPending ? "Syncing…" : "Sync now"}
        </Button>
      }
    >
      <div className="grid gap-density-3 p-density-3">
        <p className="text-xs text-muted-foreground">
          Redacted claude and codex logins mirrored to the places a sandbox reads
          them from. Values never leave this host in plain text, and nothing is
          published until a destination is configured.
        </p>

        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error instanceof Error ? error.message : String(error)}
          </p>
        )}

        {sync.data && (
          // One string rather than interleaved expressions: split across text
          // nodes the sentence reads correctly but cannot be matched as one.
          <p className="text-xs text-muted-foreground">{describeSync(sync.data)}</p>
        )}

        <ProviderStatus rows={credentials.data?.status ?? []} />

        {draft && (
          <DestinationEditor
            config={draft}
            providers={credentials.data?.providers ?? []}
            defaultSecret={credentials.data?.defaultSecret ?? ""}
            defaultMargin={credentials.data?.defaultMargin ?? ""}
            saving={save.isPending}
            onChange={setDraft}
            onSave={() => save.mutate(draft)}
          />
        )}
      </div>
    </Panel>
  );
}

/** What one publish pass did, as a sentence. */
function describeSync(result: CredentialsSyncResult): string {
  const published = (result.published ?? []).join(", ");
  const targets = (result.targets ?? []).join(", ");
  if (!published) return "Published nothing: no destination is configured.";
  return targets
    ? `Published ${published} to ${targets}.`
    : `Published ${published}.`;
}

/**
 * One row per provider, with its expiry.
 *
 * A provider that cannot be read is a row with a reason rather than a missing
 * row: "codex: not logged in" beside a healthy claude row is what tells an
 * operator which login to renew.
 */
function ProviderStatus({ rows }: { rows: CredentialStatus[] }) {
  if (rows.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">No agent logins found on this host.</p>
    );
  }
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 rounded-md border border-border bg-muted/40 p-density-3 text-xs">
      {rows.map((row) => (
        <Row key={row.provider} row={row} />
      ))}
    </dl>
  );
}

function Row({ row }: { row: CredentialStatus }) {
  return (
    <>
      <dt className="text-muted-foreground">{row.provider}</dt>
      <dd>
        {row.expired ? (
          <span className="text-destructive">expired</span>
        ) : (
          <span>{row.expiresIn ? `expires in ${row.expiresIn}` : row.source}</span>
        )}
        {(row.targets?.length ?? 0) > 0 && (
          <span className="ml-density-2 text-muted-foreground">
            → {row.targets?.join(", ")}
          </span>
        )}
      </dd>
    </>
  );
}

/**
 * The `credentials.publish` list.
 *
 * Each row is one destination, and a destination is either a host directory (a
 * docker workload bind-mounts it) or a Kubernetes Secret (a sidecar mounts it).
 * The server refuses an entry naming both or neither, so the row makes the two
 * exclusive rather than letting an operator write a refusal.
 */
function DestinationEditor({
  config,
  providers,
  defaultSecret,
  defaultMargin,
  saving,
  onChange,
  onSave,
}: {
  config: CredentialsConfig;
  providers: string[];
  defaultSecret: string;
  defaultMargin: string;
  saving: boolean;
  onChange: (config: CredentialsConfig) => void;
  onSave: () => void;
}) {
  const update = (index: number, next: CredentialDestination) =>
    onChange({
      ...config,
      publish: config.publish.map((entry, at) => (at === index ? next : entry)),
    });

  return (
    <div className="grid gap-density-3">
      <Field
        label="Refresh margin"
        htmlFor="credentials-margin"
        helper={`How far ahead of expiry a login is republished. Empty uses ${defaultMargin || "the publisher default"}.`}
      >
        <InputField
          id="credentials-margin"
          value={config.refreshMargin}
          onChange={(value: string) => onChange({ ...config, refreshMargin: value })}
          placeholder={defaultMargin}
        />
      </Field>

      {config.publish.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          <Badge>note</Badge> No destinations, so nothing is mirrored. A sandbox
          started now reaches no model provider unless it carries its own keys.
        </p>
      ) : (
        config.publish.map((destination, index) => (
          <Destination
            key={index}
            destination={destination}
            providers={providers}
            defaultSecret={defaultSecret}
            onChange={(next) => update(index, next)}
            onRemove={() =>
              onChange({
                ...config,
                publish: config.publish.filter((_, at) => at !== index),
              })
            }
          />
        ))
      )}

      <div className="flex justify-between gap-density-2">
        <div className="flex gap-density-2">
          <Button
            size="sm"
            variant="secondary"
            onClick={() =>
              onChange({ ...config, publish: [...config.publish, { directory: "" }] })
            }
          >
            Add directory
          </Button>
          <Button
            size="sm"
            variant="secondary"
            onClick={() =>
              onChange({
                ...config,
                publish: [...config.publish, { namespace: "", secret: "" }],
              })
            }
          >
            Add Secret
          </Button>
        </div>
        <Button size="sm" disabled={saving} onClick={onSave}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}

function Destination({
  destination,
  providers,
  defaultSecret,
  onChange,
  onRemove,
}: {
  destination: CredentialDestination;
  providers: string[];
  defaultSecret: string;
  onChange: (next: CredentialDestination) => void;
  onRemove: () => void;
}) {
  // Which kind this row is comes from which field it carries, because the two
  // are mutually exclusive server-side and the row was created as one or the
  // other. A directory of "" is still a directory row.
  const isDirectory = destination.directory !== undefined;

  return (
    <div className="grid gap-density-2 rounded-md border border-border p-density-3">
      {isDirectory ? (
        <Field
          label="Host directory"
          helper="Bind-mounted by a docker sandbox. Must already exist."
        >
          <InputField
            value={destination.directory ?? ""}
            onChange={(value: string) => onChange({ ...destination, directory: value })}
            placeholder="~/.captain/credentials"
          />
        </Field>
      ) : (
        <div className="grid grid-cols-2 gap-density-2">
          <Field label="Namespace" required>
            <InputField
              value={destination.namespace ?? ""}
              onChange={(value: string) => onChange({ ...destination, namespace: value })}
              placeholder="captain"
              invalid={!(destination.namespace ?? "").trim()}
            />
          </Field>
          <Field label="Secret" helper={`Empty uses ${defaultSecret}.`}>
            <InputField
              value={destination.secret ?? ""}
              onChange={(value: string) => onChange({ ...destination, secret: value })}
              placeholder={defaultSecret}
            />
          </Field>
        </div>
      )}

      <Field label="Providers" helper="Empty mirrors every configured login.">
        <Combobox
          options={providers.map((name) => ({ value: name, label: name }))}
          value={destination.providers ?? []}
          onChange={(value: string[]) => onChange({ ...destination, providers: value })}
          ariaLabel="Providers"
          multiple
          allowCustomValue={false}
          placeholder="all"
        />
      </Field>

      <div className="flex justify-end">
        <Button size="sm" variant="ghost" onClick={onRemove}>
          Remove
        </Button>
      </div>
    </div>
  );
}
