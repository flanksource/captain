import React from "react";
import { createRoot } from "react-dom/client";
import { DensityProvider, ThemeProvider } from "@flanksource/clicky-ui/hooks";
import "@flanksource/clicky-ui/styles.css";
import "./index.css";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ThemeProvider>
      <DensityProvider>
        <App />
      </DensityProvider>
    </ThemeProvider>
  </React.StrictMode>,
);
