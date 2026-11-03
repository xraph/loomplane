import { spawnSync } from "node:child_process"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { afterEach, describe, expect, it } from "vitest"

const script = resolve(import.meta.dirname, "./check-oss-bundle.mjs")
const dirs = []

function build(files) {
  const dir = mkdtempSync(join(tmpdir(), "oss-bundle-"))
  dirs.push(dir)
  mkdirSync(join(dir, "assets"))
  for (const [name, body] of Object.entries(files)) {
    writeFileSync(join(dir, "assets", name), body)
  }
  return spawnSync("node", [script, dir], { encoding: "utf8" })
}

afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

describe("check-oss-bundle", () => {
  it("passes a build without the marker", () => {
    expect(build({ "a.js": "x", "a.js.map": "{}" }).status).toBe(0)
  })

  it("fails and names code that carries the marker", () => {
    const r = build({ "a.js": 'x="__LOOMPLANE_CLOUD__"', "a.js.map": "{}" })
    expect(r.status).toBe(1)
    expect(r.stderr).toContain("code:")
  })

  it("fails when only a source map carries the marker", () => {
    const r = build({ "a.js": "x", "a.js.map": "__LOOMPLANE_CLOUD__" })
    expect(r.status).toBe(1)
    expect(r.stderr).toContain("source map only:")
    expect(r.stderr).not.toContain("code:")
  })
})
