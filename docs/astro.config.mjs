import mdx from "@astrojs/mdx";
import react from "@astrojs/react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";

export default defineConfig({
  integrations: [mdx(), react()],
  vite: {
    plugins: [tailwindcss()],
    resolve: {
      // WORKAROUND(astro-prerender-cookie-resolution): Resolve Astro's generated bare cookie import from this package instead of an unrelated ancestor package.
      // Correct fix: Astro should preserve its resolved cookie dependency when generating the prerender entry.
      // Ref: discussed with user 2026-08-23
      dedupe: ["react", "react-dom", "@tanstack/react-query", "cookie"],
    },
  },
});
