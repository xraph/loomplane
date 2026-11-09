// Fails when a public build carries Loomplane Cloud code or copy. The cloud
// entry exports the first marker below and its pages carry the rest (the
// billing routes, the plan's price line and the page addresses), so finding
// any of them under the build directory means the cloud module leaked into
// the bundle. The cloud build is expected to fail this check.
//
// Source maps are scanned too. The build emits them (build.sourcemap) and the
// web extension serves them, so a map that embeds cloud source would publish
// it even when the JS is clean. Hits are listed apart so you can tell a code
// leak from a map-only one; both fail the check.
//
//   node scripts/check-oss-bundle.mjs dist

import { readdirSync, readFileSync } from "node:fs"
import { join } from "node:path"

const MARKERS = [
  "__LOOMPLANE_CLOUD__",
  "/api/v1/billing",
  "/api/v1/platform/billing",
  "including $5 of usage",
  "settings/billing",
  "platform/billing",
]

function* files(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) yield* files(path)
    else yield path
  }
}

const dir = process.argv[2]
if (!dir) {
  console.error("usage: check-oss-bundle.mjs <build dir>")
  process.exit(2)
}

const code = []
const maps = []
const found = new Map()
try {
  for (const f of files(dir)) {
    const text = readFileSync(f, "utf8")
    const hits = MARKERS.filter((m) => text.includes(m))
    if (hits.length === 0) continue
    found.set(f, hits)
    ;(f.endsWith(".map") ? maps : code).push(f)
  }
} catch (err) {
  console.error(`check-oss-bundle: cannot read ${dir}: ${err.message}`)
  process.exit(2)
}

if (code.length > 0 || maps.length > 0) {
  console.error("Cloud code or copy found in a public build:")
  for (const f of code) console.error(`  code: ${f} (${found.get(f).join(", ")})`)
  for (const f of maps)
    console.error(`  source map only: ${f} (${found.get(f).join(", ")})`)
  process.exit(1)
}
console.log(`check-oss-bundle: no cloud code in ${dir}`)
