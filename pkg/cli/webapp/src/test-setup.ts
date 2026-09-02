import "@testing-library/jest-dom/vitest";

// jsdom does not implement scrollIntoView. Components that keep a highlighted
// row in view (e.g. the ⌘K palette) call it on every selection change, so
// provide a no-op rather than having each suite stub it.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// jsdom has no matchMedia either; clicky-ui's theme hook (used by CodeBlock)
// asks it for the dark-scheme preference on mount.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
