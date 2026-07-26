const MIN_DIRECT_ID_LENGTH = 8;

const isMac =
  typeof navigator !== "undefined" &&
  /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");

export const paletteShortcutLabel = isMac ? "⌘K" : "Ctrl K";

export function directSessionId(value: string): string | null {
  const trimmed = value.trim();
  if (trimmed.length < MIN_DIRECT_ID_LENGTH || /\s/.test(trimmed)) return null;
  return trimmed;
}
