import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useQuery } from "@tanstack/react-query";
import { Modal } from "@flanksource/clicky-ui/components";
import {
  UiCode2,
  UiSearch,
  UiTerminal,
  type IconComponent,
} from "@flanksource/clicky-ui/icons";
import { useOperations } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import {
  fetchSessionSearch,
  projectLabel,
  sessionTitle,
  shortID,
  type SessionRecord,
} from "./sessionData";
import {
  fetchPromptList,
  resolvePromptOps,
  type PromptSummary,
} from "./promptData";

// The ⌘K palette is captain's single global search: it spans sessions and
// prompts regardless of the active route and jumps to the chosen item rather
// than filtering a list in place. The per-page SearchInput on /sessions still
// owns scoped, in-place filtering; this owns "find that one thing".
//
// Session results deliberately ignore the app-bar project scope so a session is
// findable from anywhere, and the direct-open row hands the raw text to
// /sessions/{id}, whose RunSessionGet identity resolution is
// the same code path as `captain sessions get`.

const isMac =
  typeof navigator !== "undefined" &&
  /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
export const paletteShortcutLabel = isMac ? "⌘K" : "Ctrl K";

// GROUP_CAP keeps each section short so the list stays scannable; overflow is
// surfaced as a "+N more" hint rather than silently dropped.
const GROUP_CAP = 8;
const DEBOUNCE_MS = 200;

// Session identity resolution accepts a full Captain UUID or a
// provider-session-id prefix, so the direct-open row cannot gate on a UUID
// shape. Anything token-shaped and long enough to be unambiguous counts.
const MIN_DIRECT_ID_LENGTH = 8;

/**
 * Binds ⌘K / Ctrl+K to `toggle` for the lifetime of the caller. Works even while
 * a field is focused — it isn't a text-editing shortcut. `SearchInput`'s own
 * `onShortcut` cannot serve here: that listener lives in `InputField` and only
 * runs while the field is mounted, which a closed palette is not.
 */
export function useCommandPaletteShortcut(toggle: () => void) {
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (
        (event.metaKey || event.ctrlKey) &&
        !event.altKey &&
        event.key.toLowerCase() === "k"
      ) {
        event.preventDefault();
        toggle();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [toggle]);
}

export function directSessionId(value: string): string | null {
  const trimmed = value.trim();
  if (trimmed.length < MIN_DIRECT_ID_LENGTH) return null;
  if (/\s/.test(trimmed)) return null;
  return trimmed;
}

interface Row {
  key: string;
  icon: IconComponent;
  title: string;
  subtitle: string;
  meta: string;
  onSelect: () => void;
}

/**
 * Top-bar affordance that stands in for an inline search box: a click (or the
 * ⌘K/Ctrl+K shortcut) opens the palette. Rendered on every route so global
 * search is always one key away.
 */
export function SearchTrigger({ onOpen }: { onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label="Search sessions and prompts"
      className="flex w-full items-center gap-2 rounded-md border border-border bg-muted px-3 py-1.5 text-sm text-muted-foreground hover:bg-muted/70"
    >
      <UiSearch className="shrink-0 text-sm" />
      <span className="flex-1 truncate text-left">
        Search sessions, prompts, or an id…
      </span>
      <kbd className="shrink-0 rounded border border-border bg-background px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-muted-foreground">
        {paletteShortcutLabel}
      </kbd>
    </button>
  );
}

export function CommandPalette({
  open,
  onClose,
  onNavigate,
}: {
  open: boolean;
  onClose: () => void;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
}) {
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Reset to a clean slate every time the palette opens, then move focus to the
  // input so the user can type immediately. The Modal focuses its own panel in a
  // passive effect whose flush time varies (opening cascades state updates), so a
  // single deferred focus keeps losing the race. Instead retry briefly until the
  // input holds focus, then stop — there is no focus trap, so once it lands it
  // stays. This self-corrects regardless of when the Modal grabs focus.
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setDebounced("");
    setActive(0);
    let tries = 0;
    const timer = setInterval(() => {
      const el = inputRef.current;
      if (el && document.activeElement !== el) el.focus();
      if (++tries >= 10 || (el && document.activeElement === el))
        clearInterval(timer);
    }, 30);
    return () => clearInterval(timer);
  }, [open]);

  // Results are server-side, so hold off a beat rather than firing a request per
  // keystroke.
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(query.trim()), DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [query]);

  const enabled = open && debounced.length > 0;

  const sessionsQuery = useQuery({
    queryKey: ["palette-sessions", debounced],
    queryFn: () => fetchSessionSearch({ query: debounced, limit: 20 }),
    enabled,
  });

  const { operations } = useOperations(apiClient);
  const promptOps = useMemo(() => resolvePromptOps(operations), [operations]);
  const promptsQuery = useQuery({
    queryKey: ["palette-prompts", promptOps.list?.path, debounced],
    queryFn: () =>
      fetchPromptList(promptOps.list!, { source: "all", query: debounced }),
    enabled: enabled && Boolean(promptOps.list),
  });

  const sessions = sessionsQuery.data?.sessions ?? EMPTY_SESSIONS;
  const prompts = promptsQuery.data ?? EMPTY_PROMPTS;
  const directId = directSessionId(query);

  // One flat, ordered list (direct id, sessions, then prompts) backs keyboard
  // navigation; the rendered groups index into it so the highlight and Enter
  // stay in sync.
  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];
    if (directId) {
      out.push({
        key: `id:${directId}`,
        icon: UiSearch,
        title: "Open session by id",
        subtitle: directId,
        meta: "id",
        onSelect: () => {
          onClose();
          onNavigate(`/sessions/${encodeURIComponent(directId)}`);
        },
      });
    }
    for (const session of sessions.slice(0, GROUP_CAP)) {
      out.push({
        key: `session:${session.key}`,
        icon: UiTerminal,
        title: sessionTitle(session),
        subtitle: shortID(session.id) || session.key,
        meta: session.project ? projectLabel(session.project) : session.source,
        onSelect: () => {
          onClose();
          onNavigate(`/sessions/${encodeURIComponent(session.key)}`);
        },
      });
    }
    for (const prompt of prompts.slice(0, GROUP_CAP)) {
      out.push({
        key: `prompt:${prompt.id}`,
        icon: UiCode2,
        title: prompt.name,
        subtitle: prompt.relPath || prompt.path,
        meta: prompt.sourceKind,
        onSelect: () => {
          onClose();
          onNavigate(`/prompts/${encodeURIComponent(prompt.id)}`);
        },
      });
    }
    return out;
  }, [directId, sessions, prompts, onClose, onNavigate]);

  // Keep the active index in range as results change while typing.
  useEffect(() => {
    setActive((a) => (rows.length === 0 ? 0 : Math.min(a, rows.length - 1)));
  }, [rows.length]);

  // Scroll the highlighted row into view as the selection moves.
  useEffect(() => {
    listRef.current
      ?.querySelector<HTMLElement>(`[data-row="${active}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [active]);

  function onKeyDown(event: KeyboardEvent) {
    if (rows.length === 0) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((a) => (a + 1) % rows.length);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((a) => (a - 1 + rows.length) % rows.length);
    } else if (event.key === "Enter") {
      event.preventDefault();
      rows[active]?.onSelect();
    }
  }

  const directBase = directId ? 1 : 0;
  const promptBase = directBase + Math.min(sessions.length, GROUP_CAP);
  const loading = sessionsQuery.isFetching || promptsQuery.isFetching;

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      hideClose
      expandable={false}
      closeOnBackdrop
      closeOnEsc
      scrollBody={false}
    >
      <div className="flex items-center gap-2 border-b border-border pb-3">
        <UiSearch className="shrink-0 text-base text-muted-foreground" />
        <input
          ref={inputRef}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
          placeholder="Search sessions, prompts, or paste a session id…"
          aria-label="Search sessions and prompts"
          className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        />
        <kbd className="shrink-0 rounded border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
          esc
        </kbd>
      </div>

      <div ref={listRef} className="max-h-[60vh] overflow-y-auto pt-2">
        {!query.trim() ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            Type to search sessions and prompts, or paste a session id.
          </div>
        ) : rows.length === 0 && !loading ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            No sessions or prompts match “{query.trim()}”.
          </div>
        ) : (
          <>
            {directId && (
              <Group label="Session id" overflow={0}>
                <PaletteRow
                  row={rows[0]!}
                  index={0}
                  active={active}
                  onHover={setActive}
                />
              </Group>
            )}
            {sessions.length > 0 && (
              <Group label="Sessions" overflow={sessions.length - GROUP_CAP}>
                {sessions.slice(0, GROUP_CAP).map((_, i) => (
                  <PaletteRow
                    key={rows[directBase + i]!.key}
                    row={rows[directBase + i]!}
                    index={directBase + i}
                    active={active}
                    onHover={setActive}
                  />
                ))}
              </Group>
            )}
            {prompts.length > 0 && (
              <Group label="Prompts" overflow={prompts.length - GROUP_CAP}>
                {prompts.slice(0, GROUP_CAP).map((_, i) => (
                  <PaletteRow
                    key={rows[promptBase + i]!.key}
                    row={rows[promptBase + i]!}
                    index={promptBase + i}
                    active={active}
                    onHover={setActive}
                  />
                ))}
              </Group>
            )}
            {loading && (
              <div className="px-2 py-2 text-xs text-muted-foreground">
                Searching…
              </div>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}

function Group({
  label,
  overflow,
  children,
}: {
  label: string;
  overflow: number;
  children: ReactNode;
}) {
  return (
    <div className="mb-1">
      <div className="flex items-center justify-between px-2 py-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        <span>{label}</span>
        {overflow > 0 && (
          <span className="normal-case tracking-normal">+{overflow} more</span>
        )}
      </div>
      {children}
    </div>
  );
}

function PaletteRow({
  row,
  index,
  active,
  onHover,
}: {
  row: Row;
  index: number;
  active: number;
  onHover: (index: number) => void;
}) {
  const isActive = index === active;
  const Icon = row.icon;
  return (
    <button
      type="button"
      data-row={index}
      onMouseMove={() => onHover(index)}
      onClick={row.onSelect}
      className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left ${
        isActive ? "bg-primary/10" : "hover:bg-muted"
      }`}
    >
      <Icon className="shrink-0 text-sm text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate text-sm text-foreground">
        {row.title}
      </span>
      {row.meta && (
        <span className="shrink-0 truncate text-[11px] text-muted-foreground">
          {row.meta}
        </span>
      )}
      <span className="max-w-[12rem] shrink-0 truncate text-[11px] tabular-nums text-muted-foreground">
        {row.subtitle}
      </span>
    </button>
  );
}

const EMPTY_SESSIONS: SessionRecord[] = [];
const EMPTY_PROMPTS: PromptSummary[] = [];
