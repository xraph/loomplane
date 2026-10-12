#!/usr/bin/env node
// Writes specs/authsome.openapi.json: the part of authsome's API the
// dashboard generates a Forge client for, with every path moved under the
// prefix Loomplane mounts authsome at. Ported from tap
// (github.com/xraph/tap-frontend, packages/client/scripts/authsome-spec.mjs).
//
// Loomplane runs authsome in-process on a router of its own, so its routes
// aren't in Loomplane's /openapi.json; authsome's SDK spec is the source.
//
// Usage (from apps/dashboard):
//   pnpm gen:authsome-spec
// which reads the spec of the authsome version Loomplane's go.mod pins. Or:
//   AUTHSOME_SPEC=/path/to/authsome/sdkgen/spec.json node scripts/authsome-spec.mjs
//
// AUTHSOME_MOUNT overrides the prefix (default /authsome, identity.base_path).

import { execFileSync } from "node:child_process"
import { readFile, writeFile } from "node:fs/promises"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const output = join(here, "..", "specs", "authsome.openapi.json")
const mount = (process.env.AUTHSOME_MOUNT ?? "/authsome").replace(/\/+$/, "")

// The organization surface. Sign-in, sessions, MFA and the rest stay with the
// @authsome/ui-* SDK, which owns the session; generating them here would give
// the dashboard two clients fighting over one session. Teams are left out:
// Loomplane has no use for groups inside an organization yet.
const KEEP = [/^\/v1\/orgs(?!\/[^/]+\/teams)(\/|$)/, /^\/v1\/me\/switch-org$/]

function pinnedSpec() {
  // The authsome module the backend builds against, from Go's module cache.
  const dir = execFileSync(
    "go",
    ["list", "-m", "-f", "{{.Dir}}", "github.com/xraph/authsome"],
    { cwd: join(here, "..", "..", ".."), encoding: "utf8" }
  ).trim()
  return join(dir, "sdkgen", "spec.json")
}

const source = process.env.AUTHSOME_SPEC ?? pinnedSpec()
const raw = /^https?:\/\//.test(source)
  ? await (await fetch(source)).text()
  : await readFile(source, "utf8")
const spec = JSON.parse(raw)

const paths = {}
for (const [path, item] of Object.entries(spec.paths ?? {})) {
  if (KEEP.some((pattern) => pattern.test(path))) {
    paths[`${mount}${path}`] = item
  }
}
spec.paths = paths

// Drop schemas nothing kept refers to, so the committed file stays small
// enough to review when authsome changes.
const schemas = spec.components?.schemas ?? {}
const reachable = new Set()
const visit = (node) => {
  if (Array.isArray(node)) {
    node.forEach(visit)
    return
  }
  if (!node || typeof node !== "object") {
    return
  }
  const ref = node.$ref
  if (typeof ref === "string" && ref.startsWith("#/components/schemas/")) {
    const name = ref.slice("#/components/schemas/".length)
    if (!reachable.has(name)) {
      reachable.add(name)
      visit(schemas[name])
    }
  }
  Object.values(node).forEach(visit)
}
visit(spec.paths)
spec.components.schemas = Object.fromEntries(
  Object.entries(schemas)
    .filter(([name]) => reachable.has(name))
    .sort(([a], [b]) => a.localeCompare(b))
)

await writeFile(output, `${JSON.stringify(spec, null, 2)}\n`)
console.log(
  `wrote ${Object.keys(paths).length} paths and ${reachable.size} schemas from ${source} to ${output}`
)
