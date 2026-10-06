import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
const root = fileURLToPath(new URL("../dist/", import.meta.url));
const mime = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
};
createServer(async (req, res) => {
  try {
    let pathname = decodeURIComponent(
      new URL(req.url, "http://localhost").pathname,
    );
    // Serve both root and the real GitHub Pages base path for local verification.
    if (pathname === "/turaco") {
      res.writeHead(302, { Location: "/turaco/" }).end();
      return;
    }
    if (pathname.startsWith("/turaco/")) pathname = pathname.slice(7);
    let file = path.resolve(root, `.${pathname}`);
    if (file !== path.resolve(root) && !file.startsWith(root))
      throw new Error("Invalid path");
    if ((await stat(file)).isDirectory()) file = path.join(file, "index.html");
    const content = await readFile(file);
    res
      .writeHead(200, {
        "Content-Type": mime[path.extname(file)] || "application/octet-stream",
        "X-Content-Type-Options": "nosniff",
      })
      .end(content);
  } catch {
    res.writeHead(404, { "Content-Type": "text/plain" }).end("Not found");
  }
}).listen(4173, "127.0.0.1", () =>
  console.log(
    "Turaco preview: http://localhost:4173/turaco/ (also available at /)",
  ),
);
