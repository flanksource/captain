import { Button, Modal } from "@flanksource/clicky-ui/components";
import { Icon, UiTrash } from "@flanksource/clicky-ui/data";
import type { PromptSummary } from "./promptData";

export function PromptDeleteDialog({
  prompt,
  open,
  loading,
  onConfirm,
  onClose,
}: {
  prompt: PromptSummary;
  open: boolean;
  loading: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  if (!open) return null;
  return (
    <Modal
      open
      onClose={onClose}
      title={`Delete ${prompt.name}?`}
      footer={
        <div className="flex justify-end gap-density-2">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="border-destructive/40 text-destructive"
            loading={loading}
            onClick={onConfirm}
          >
            <Icon icon={UiTrash} className="size-4" />
            Delete prompt
          </Button>
        </div>
      }
    >
      <p className="text-sm">
        This removes <code className="font-mono text-xs">{prompt.path}</code>{" "}
        from disk. There is no undo.
      </p>
    </Modal>
  );
}
