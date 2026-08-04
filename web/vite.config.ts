import react from "@vitejs/plugin-react";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react(), pairingPageDevPlugin()],
  build: {
    outDir: "../internal/server/webassets/dist",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
});

function pairingPageDevPlugin(): Plugin {
  const directory = fileURLToPath(new URL("../internal/server/pairing-web/", import.meta.url));
  const assets: Record<string, { file: string; contentType: string }> = {
    "/pair": { file: "index.html", contentType: "text/html; charset=utf-8" },
    "/pair/": { file: "index.html", contentType: "text/html; charset=utf-8" },
    "/pair/app.js": { file: "app.js", contentType: "text/javascript; charset=utf-8" },
    "/pair/catalog.generated.js": { file: "catalog.generated.js", contentType: "text/javascript; charset=utf-8" },
    "/pair/session.js": { file: "session.js", contentType: "text/javascript; charset=utf-8" },
    "/pair/storage.js": { file: "storage.js", contentType: "text/javascript; charset=utf-8" },
    "/pair/style.css": { file: "style.css", contentType: "text/css; charset=utf-8" },
    "/pair/logo.svg": { file: "logo.svg", contentType: "image/svg+xml" },
  };
  return {
    name: "yuanshu-pairing-page-dev",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use(async (request, response, next) => {
        const asset = assets[(request.url ?? "").split("?", 1)[0]];
        if (!asset) {
          next();
          return;
        }
        try {
          const body = await readFile(`${directory}${asset.file}`);
          response.statusCode = 200;
          response.setHeader("Content-Type", asset.contentType);
          response.setHeader("Cache-Control", "no-store");
          response.end(body);
        } catch (error) {
          next(error as Error);
        }
      });
    },
  };
}
