import React, { Suspense } from "react";
import {
  renderToReadableStream,
  decodeReply,
  loadServerAction,
} from "@vitejs/plugin-rsc/rsc/server";
import { Interaction } from "./interaction.jsx";
import "./style.css";

async function DelayedContent() {
  await new Promise((resolve) => setTimeout(resolve, 150));
  return <p>Streamed from the server.</p>;
}

export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (request.method === "POST") {
      if (
        request.headers.get("origin") !==
        new URL(process.env.PUBLIC_ORIGIN || url.origin).origin
      )
        return new Response("Forbidden", { status: 403 });
      const id = request.headers.get("x-rsc-action");
      if (!id?.endsWith("#greet"))
        return new Response("Unknown action", { status: 400 });
      try {
        const args = await decodeReply(await request.text());
        const action = await loadServerAction(id);
        return new Response(renderToReadableStream(await action(...args)), {
          headers: { "content-type": "text/x-component" },
        });
      } catch {
        return new Response("Action failed", { status: 500 });
      }
    }
    if (request.method !== "GET")
      return new Response("Method not allowed", { status: 405 });
    const tree = (
      <html lang="en">
        <head>
          <meta charSet="utf-8" />
          <meta name="viewport" content="width=device-width,initial-scale=1" />
          <title>Vite RSC on Loomplane</title>
        </head>
        <body>
          <main>
            <h1>Vite RSC on Loomplane</h1>
            <p>Path: {url.pathname.replace(/\.rsc$/, "") || "/"}</p>
            <Interaction />
            <Suspense fallback={<p>Loading server content…</p>}>
              <DelayedContent />
            </Suspense>
          </main>
        </body>
      </html>
    );
    const flight = renderToReadableStream(tree);
    if (url.pathname.endsWith(".rsc"))
      return new Response(flight, {
        headers: { "content-type": "text/x-component" },
      });
    const ssr = await import.meta.viteRsc.loadModule("ssr", "index");
    return new Response(await ssr.html(flight), {
      headers: { "content-type": "text/html" },
    });
  },
};
