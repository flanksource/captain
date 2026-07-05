import { CodeBlock } from "@flanksource/clicky-ui/data";
import { ClickyProviders } from "./ClickyProviders";

export type CodeExampleProps = {
  language: string;
  source: string;
  title?: string;
};

export default function CodeExample({ language, source, title }: CodeExampleProps) {
  return (
    <ClickyProviders>
      <div className="my-5">
        {title ? (
          <div className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {title}
          </div>
        ) : null}
        <CodeBlock language={language} source={source.trim()} />
      </div>
    </ClickyProviders>
  );
}

