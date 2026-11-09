import {
  createFromFetch,
  encodeReply,
  setServerCallback,
} from "@vitejs/plugin-rsc/browser";
import { hydrateRoot } from "react-dom/client";

setServerCallback(async (id, args) => {
  const response = await fetch(location.href, {
    method: "POST",
    headers: { "x-rsc-action": id },
    body: await encodeReply(args),
  });
  if (!response.ok) throw new Error("Server function failed");
  return createFromFetch(Promise.resolve(response));
});

const endpoint = new URL(location.href);
endpoint.pathname += ".rsc";
const root = await createFromFetch(fetch(endpoint));
hydrateRoot(document, root);
