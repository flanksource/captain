import mdx from "@astrojs/mdx";
import react from "@astrojs/react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";

export default defineConfig({
  integrations: [mdx(), react()],
  vite: {
    plugins: [tailwindcss()],
    resolve: {
      dedupe: ["react", "react-dom", "@tanstack/react-query"],
    },
  },
});

