// Fails when a public build carries Loomplane Cloud code. The cloud entry
// exports the marker below, so finding it under the build directory means the
// cloud module leaked into the bundle.
//
//   node scripts/check-oss-bundle.mjs dist

import { readdirSync, readFileSync } from "node:fs"
import { join } from "node:path"

const MARKER = "__LOOMPLANE_CLOUD__"

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

let found
try {
  found = [...files(dir)].filter((f) => readFileSync(f).includes(MARKER))
} catch (err) {
  console.error(`check-oss-bundle: cannot read ${dir}: ${err.message}`)
  process.exit(2)
}

if (found.length > 0) {
  console.error(`${MARKER} found in a public build:`)
  for (const f of found) console.error(`  ${f}`)
  process.exit(1)
}
console.log(`check-oss-bundle: no cloud code in ${dir}`)
