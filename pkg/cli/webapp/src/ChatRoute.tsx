import { useEffect } from "react";
import { Button } from "@flanksource/clicky-ui/components";
import { useChatWindowManager } from "@flanksource/clicky-ui/ai";

type ChatRouteProps = {
  threadId: string;
  model?: string;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
};

export function ChatRoute({ threadId, model, onNavigate }: ChatRouteProps) {
  const { panels, openPanel } = useChatWindowManager();

  useEffect(() => {
    if (!threadId) return;
    if (panels.some((panel) => panel.threadId === threadId)) return;
    openPanel({ threadId, initialModel: model || null });
  }, [model, openPanel, panels, threadId]);

  return (
    <div className="flex h-full min-h-0 items-center justify-center p-6">
      <div className="flex flex-col items-center gap-3 text-center">
        <div className="text-sm font-medium">Chat window opened</div>
        <Button size="sm" variant="outline" onClick={() => onNavigate("/")}>
          New agent
        </Button>
      </div>
    </div>
  );
}
