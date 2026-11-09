import { serve } from "@hono/node-server";
import { serveStatic } from "@hono/node-server/serve-static";
import { Hono } from "hono";
import handler from "./dist/rsc/index.js";

const app = new Hono();
app.get("/health", (c) => c.text("ok"));
app.use("/assets/*", serveStatic({ root: "./dist/client" }));
app.all("*", (c) => handler.fetch(c.req.raw));

serve({
  fetch: app.fetch,
  hostname: "0.0.0.0",
  port: Number(process.env.PORT || 3000),
});
