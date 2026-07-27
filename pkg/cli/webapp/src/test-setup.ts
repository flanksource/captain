import "@testing-library/jest-dom/vitest";

// jsdom does not implement scrollIntoView. Components that keep a highlighted
// row in view (e.g. the ⌘K palette) call it on every selection change, so
// provide a no-op rather than having each suite stub it.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}
