import type { ReactNode } from "react";
import { DensityProvider, ThemeProvider } from "@flanksource/clicky-ui/hooks";

export function ClickyProviders({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider>
      <DensityProvider>{children}</DensityProvider>
    </ThemeProvider>
  );
}

