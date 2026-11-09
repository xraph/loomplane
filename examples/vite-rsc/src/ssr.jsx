import {
  createFromReadableStream,
  getClientEntryUrl,
} from "@vitejs/plugin-rsc/ssr";
import { renderToReadableStream } from "react-dom/server.edge";

export async function html(flight) {
  return renderToReadableStream(await createFromReadableStream(flight), {
    bootstrapModules: [getClientEntryUrl()],
  });
}
