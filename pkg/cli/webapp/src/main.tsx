import React from "react";
import { createRoot } from "react-dom/client";
import { ErrorWrapper } from "@flanksource/clicky-ui/components";
import { DensityProvider, ThemeProvider } from "@flanksource/clicky-ui/hooks";
import { MonacoProvider } from "@flanksource/clicky-ui/monaco";
import { clickyIconProvider } from "@flanksource/clicky-ui/icons";
import { setFallbackIconProvider } from "@flanksource/clicky-ui/data";
import "@flanksource/clicky-ui/styles.css";
import "./index.css";
import { App } from "./App";
import { getMonacoWorker } from "./monacoWorkers";

setFallbackIconProvider(clickyIconProvider());

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorWrapper>
      <ThemeProvider>
        <DensityProvider>
          <MonacoProvider getWorker={getMonacoWorker}>
            <App />
          </MonacoProvider>
        </DensityProvider>
      </ThemeProvider>
    </ErrorWrapper>
  </React.StrictMode>,
);
