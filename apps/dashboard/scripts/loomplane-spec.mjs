#!/usr/bin/env node
// Writes specs/loomplane.openapi.json from Loomplane's OpenAPI document on
// stdin, keeping only the /api paths the dashboard calls (Forge also
// publishes its health and metrics routes) and dropping the servers block,
// which names whichever machine produced it.
//
// Usage: curl -fsS http://localhost:8780/openapi.json | node scripts/loomplane-spec.mjs

import { writeFile } from "node:fs/promises"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const output = join(here, "..", "specs", "loomplane.openapi.json")

let raw = ""
for await (const chunk of process.stdin) raw += chunk
const spec = JSON.parse(raw)
spec.paths = Object.fromEntries(
  Object.entries(spec.paths ?? {}).filter(([path]) => path.startsWith("/api/"))
)
spec.servers = []
await writeFile(output, `${JSON.stringify(spec, null, 2)}\n`)
console.log(`wrote ${Object.keys(spec.paths).length} paths to ${output}`)
