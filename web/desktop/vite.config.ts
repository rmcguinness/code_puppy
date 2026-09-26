import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const outDir = "../../cmd/blitz-desktop/dist";

// The desktop binary embeds the build, and go:embed only reaches files
// under its own package, so the build goes there. Emptying it removes the
// committed placeholder that lets the Go package compile before any build,
// so it's written back.
export default defineConfig({
  plugins: [
    react(),
    {
      name: "keep-placeholder",
      closeBundle() {
        writeFileSync(resolve(__dirname, outDir, ".gitkeep"), "");
      },
    },
  ],
  build: { outDir, emptyOutDir: true },
});
