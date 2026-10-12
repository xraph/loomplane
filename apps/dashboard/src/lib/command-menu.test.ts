import { describe, expect, it } from "vitest"

import type { Source } from "@/client/forge/loomplane/types"
import { analyzeItems, projectIDFromPath } from "@/lib/command-menu"

const source = (alias: string, extra: Partial<Source> = {}): Source => ({
  alias,
  createdAt: "",
  createdBy: "",
  id: `src_${alias}`,
  orgId: "org",
  projectId: "prj",
  ref: "",
  repository: `acme/${alias}`,
  root: "",
  updatedAt: "",
  ...extra,
})

describe("projectIDFromPath", () => {
  it("reads the project from its page and the pages under it", () => {
    expect(projectIDFromPath("/projects/prj_1")).toBe("prj_1")
    expect(projectIDFromPath("/projects/prj_1/plans")).toBe("prj_1")
  })

  it("gives nothing elsewhere", () => {
    expect(projectIDFromPath("/")).toBeUndefined()
    expect(projectIDFromPath("/projects")).toBeUndefined()
    expect(projectIDFromPath("/members")).toBeUndefined()
  })
})

describe("analyzeItems", () => {
  it("offers nothing to someone who can't run analyses", () => {
    expect(analyzeItems([source("web"), source("api")], false)).toEqual([])
  })

  it("offers one source on its own, without an all entry", () => {
    const items = analyzeItems([source("web")], true)
    expect(items.map((i) => i.label)).toEqual(["Analyze web"])
    expect(items[0]).toMatchObject({ alias: "web", detail: "acme/web" })
  })

  it("adds an all entry from two sources", () => {
    const items = analyzeItems([source("web"), source("api")], true)
    expect(items.map((i) => i.label)).toEqual([
      "Analyze web",
      "Analyze api",
      "Analyze all repositories",
    ])
    expect(items[2].alias).toBeUndefined()
  })

  it("skips removed sources and counts only the live ones", () => {
    const gone = source("old", { removedAt: "2026-10-01T00:00:00Z" })
    const items = analyzeItems([source("web"), gone], true)
    expect(items.map((i) => i.label)).toEqual(["Analyze web"])
  })

  it("shows the directory with the repository", () => {
    const items = analyzeItems([source("web", { root: "apps/web" })], true)
    expect(items[0].detail).toBe("acme/web/apps/web")
  })

  it("offers nothing for a project with no sources", () => {
    expect(analyzeItems([], true)).toEqual([])
  })
})
