import { describe, expect, it } from "vitest"

import { messageOf, statusOf } from "@/client/errors"
import { HTTPError } from "@/client/forge/loomplane/fetch"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { repoLabel } from "@/lib/format"
import { slugify } from "@/lib/slug"
import { weftRows } from "@/pages/project-detail"

describe("slugify", () => {
  // The same cases as TestSlugify in internal/projects: the dashboard's
  // preview must match the slug the API will choose.
  it.each([
    ["Difference Engine", "difference-engine"],
    ["  API -- Gateway!! ", "api-gateway"],
    ["Ünïcödé Service", "n-c-d-service"],
    ["already-a-slug", "already-a-slug"],
    ["!!!", ""],
    ["ab ".repeat(30), "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab"],
    ["Service 2 (EU) / Frontend", "service-2-eu-frontend"],
  ])("slugify(%j) is %j", (name, want) => {
    expect(slugify(name)).toBe(want)
  })
})

const project = (status: string): Project => ({
  id: "proj_1",
  orgId: "aorg_1",
  name: "Engine",
  slug: "engine",
  description: "",
  repositoryUrl: "",
  status,
  createdBy: "ausr_1",
  createdAt: "2026-10-12T03:10:26Z",
  updatedAt: "2026-10-12T03:10:26Z",
})

const env = (name: string): Environment => ({
  id: `env_${name}`,
  orgId: "aorg_1",
  projectId: "proj_1",
  name,
  kind: name,
  createdAt: "2026-10-12T03:10:26Z",
  updatedAt: "2026-10-12T03:10:26Z",
})

describe("weftRows", () => {
  it("stands the defaults in while the environments load", () => {
    expect(weftRows(project("initializing"), [])).toEqual([
      { name: "production", ready: false },
      { name: "preview", ready: false },
    ])
  })

  it("marks environments ready once the project is", () => {
    expect(
      weftRows(project("ready"), [env("production"), env("preview")])
    ).toEqual([
      { name: "production", ready: true },
      { name: "preview", ready: true },
    ])
  })

  it("shows the environments the project has, and only those", () => {
    const rows = weftRows(project("ready"), [env("production"), env("staging")])
    expect(rows.map((r) => r.name)).toEqual(["production", "staging"])
  })
})

describe("errors", () => {
  it("reads the status and the server's message", () => {
    const err = new HTTPError(
      409,
      "a project with that slug already exists in this organization",
      "",
      {}
    )
    expect(statusOf(err)).toBe(409)
    expect(messageOf(err, "fallback")).toBe(
      "A project with that slug already exists in this organization."
    )
  })

  it("falls back for what it can't explain", () => {
    expect(statusOf(new Error("boom"))).toBe(0)
    expect(messageOf(undefined, "Try again.")).toBe("Try again.")
    expect(messageOf(new HTTPError(502, "", "", {}), "x")).toBe(
      "Loomplane couldn't finish that. Try again in a moment."
    )
  })
})

describe("repoLabel", () => {
  it.each([
    ["https://github.com/acme/storefront", "github.com/acme/storefront"],
    ["git@github.com:acme/storefront.git", "github.com/acme/storefront"],
  ])("repoLabel(%j) is %j", (url, want) => {
    expect(repoLabel(url)).toBe(want)
  })
})
