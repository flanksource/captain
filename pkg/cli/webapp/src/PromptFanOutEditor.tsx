import { useId, useState } from "react";
import { Button, Field, InputField } from "@flanksource/clicky-ui/components";
import { errorMessage } from "./promptWorkbenchApi";
import { readPromptFanOut, updatePromptFanOutSource } from "./promptSchemaSource";

export function PromptFanOutEditor({ source, onChange, onEditSource }: {
  source: string;
  onChange: (source: string) => void;
  onEditSource: () => void;
}) {
  try {
    return <FanOutFields key={source} source={source} initial={readPromptFanOut(source)} onChange={onChange} />;
  } catch (error) {
    return (
      <div className="space-y-density-3">
        <p role="alert" className="text-sm text-destructive">{errorMessage(error)}</p>
        <Button size="sm" variant="outline" onClick={onEditSource}>Edit source</Button>
      </div>
    );
  }
}

function FanOutFields({ source, initial, onChange }: {
  source: string;
  initial: ReturnType<typeof readPromptFanOut>;
  onChange: (source: string) => void;
}) {
  const id = useId();
  const [script, setScript] = useState(initial?.exec ?? "");
  const [workers, setWorkers] = useState(String(initial?.concurrency || ""));
  const [showScriptError, setShowScriptError] = useState(false);
  const [showWorkersError, setShowWorkersError] = useState(false);
  const concurrency = Number(workers);
  const validScript = Boolean(script.trim());
  const validWorkers = workers === "" || (/^\d+$/.test(workers) && Number.isSafeInteger(concurrency) && concurrency >= 1);
  const scriptError = showScriptError && !validScript;
  const workersError = showWorkersError && !validWorkers;

  return (
    <form
      className="max-w-2xl space-y-density-4"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        setShowScriptError(true);
        setShowWorkersError(true);
        if (validScript && validWorkers) {
          onChange(updatePromptFanOutSource(source, workers ? { exec: script, concurrency } : { exec: script }));
        }
      }}
    >
      <p className="text-sm text-muted-foreground">The script runs once on the Captain host. Captain starts one session per JSON array item it prints.</p>
      <Field label="Script" htmlFor={`${id}-script`} required>
        <InputField
          id={`${id}-script`}
          as="textarea"
          rows={8}
          required
          value={script}
          onChange={setScript}
          onBlur={() => setShowScriptError(true)}
          invalid={scriptError}
          aria-describedby={scriptError ? `${id}-script-error` : `${id}-script-help`}
          inputClassName="font-mono"
        />
        <p id={`${id}-script-help`} className="text-xs text-muted-foreground">Read variables as JSON from stdin. Print one JSON array to stdout. The prompt can use <code>{"{{item}}"}</code>.</p>
        {scriptError && <p id={`${id}-script-error`} role="alert" className="text-sm text-destructive">Enter a script.</p>}
      </Field>
      <Field label="Concurrent sessions (optional)" htmlFor={`${id}-workers`}>
        <InputField
          id={`${id}-workers`}
          inputMode="numeric"
          value={workers}
          onChange={setWorkers}
          onBlur={() => setShowWorkersError(true)}
          invalid={workersError}
          aria-describedby={workersError ? `${id}-workers-error` : `${id}-workers-help`}
          className="max-w-32"
        />
        <p id={`${id}-workers-help`} className="text-xs text-muted-foreground">Defaults to 1.</p>
        {workersError && <p id={`${id}-workers-error`} role="alert" className="text-sm text-destructive">Enter a whole number of at least 1.</p>}
      </Field>
      <div className="flex flex-wrap gap-density-2">
        <Button type="submit" size="sm">Apply to draft</Button>
        {initial && <Button type="button" size="sm" variant="ghost" onClick={() => onChange(updatePromptFanOutSource(source))}>Remove from draft</Button>}
      </div>
    </form>
  );
}
