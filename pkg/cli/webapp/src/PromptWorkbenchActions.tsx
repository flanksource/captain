import { Button } from "@flanksource/clicky-ui/components";
import { Icon, UiCopy, UiRefresh, UiTrash } from "@flanksource/clicky-ui/data";
import { RunningPromptsBadge } from "./RunningPrompts";
import { PromptWriteAction } from "./PromptWriteModal";
import type { PromptDetail } from "./promptData";

/** The header toolbar: running-run badge, Duplicate, Delete, Save / Save as…, Refresh. */
export function PromptWorkbenchActions({
  detail,
  scratch,
  canCreate,
  canUpdate,
  canDelete,
  canSave,
  canSaveAs,
  saving,
  onSelectRun,
  onDuplicate,
  onDelete,
  onSave,
  onSaveAs,
  onRefresh,
}: {
  detail?: PromptDetail;
  scratch: boolean;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
  canSave: boolean;
  canSaveAs: boolean;
  saving: boolean;
  onSelectRun: (id: string) => void;
  onDuplicate: () => void;
  onDelete: () => void;
  onSave: () => void;
  onSaveAs: () => void;
  onRefresh: () => void;
}) {
  const editable = Boolean(detail) && !scratch;
  return (
    <div className="flex items-center gap-density-2">
      <RunningPromptsBadge onSelectRun={onSelectRun} />
      {editable && canCreate && (
        <Button
          size="sm"
          variant="ghost"
          onClick={onDuplicate}
          title="Create a copy of this prompt in a writable source"
        >
          <Icon icon={UiCopy} className="size-4" />
          Duplicate
        </Button>
      )}
      {editable && detail?.writable && canDelete && (
        <Button size="sm" variant="ghost" onClick={onDelete}>
          <Icon icon={UiTrash} className="size-4" />
          Delete
        </Button>
      )}
      {editable &&
        detail &&
        ((detail.writable && canUpdate) || (!detail.writable && canCreate)) && (
          <PromptWriteAction
            writable={detail.writable}
            disabled={detail.writable ? !canSave : !canSaveAs}
            loading={detail.writable && saving}
            onClick={detail.writable ? onSave : onSaveAs}
          />
        )}
      <Button size="sm" variant="outline" onClick={onRefresh}>
        <Icon icon={UiRefresh} className="size-4" />
        Refresh
      </Button>
    </div>
  );
}
