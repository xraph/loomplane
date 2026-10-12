import { describe, expect, it } from "vitest"

import { messageOf, statusOf } from "@/client/errors"
import { HTTPError } from "@/client/forge/loomplane/fetch"
import { githubRepo, repoLabel, repositoryURL } from "@/lib/format"
import { colorNames, iconNames, projectStyle } from "@/lib/project-style"
import { slugify } from "@/lib/slug"

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

describe("githubRepo", () => {
  it.each([
    ["https://github.com/acme/storefront", "acme/storefront"],
    ["https://github.com/acme/storefront.git", "acme/storefront"],
    ["https://github.com/acme/store.front/", "acme/store.front"],
    ["git@github.com:acme/storefront.git", "acme/storefront"],
    ["https://gitlab.com/acme/storefront", ""],
    ["acme/storefront", ""],
  ])("githubRepo(%j) is %j", (url, want) => {
    expect(githubRepo(url)).toBe(want)
  })
})

describe("repositoryURL", () => {
  it.each([
    ["acme/storefront", "https://github.com/acme/storefront"],
    [" acme/storefront ", "https://github.com/acme/storefront"],
    [
      "git@github.com:acme/storefront.git",
      "git@github.com:acme/storefront.git",
    ],
    ["https://gitlab.com/acme/x", "https://gitlab.com/acme/x"],
  ])("repositoryURL(%j) is %j", (input, want) => {
    expect(repositoryURL(input)).toBe(want)
  })
})

describe("projectStyle", () => {
  it("keeps the icon and color the server sent", () => {
    expect(projectStyle({ id: "p1", icon: "rocket", color: "teal" })).toEqual({
      icon: "rocket",
      color: "teal",
    })
  })

  it("falls back to the same pick for the same ID", () => {
    const a = projectStyle({ id: "proj_abc", icon: "unicorn", color: "" })
    const b = projectStyle({ id: "proj_abc" })
    expect(a).toEqual(b)
    expect(iconNames).toContain(a.icon)
    expect(colorNames).toContain(a.color)
  })

  it("spreads different IDs over the icons", () => {
    const icons = new Set(
      Array.from({ length: 40 }, (_, i) => projectStyle({ id: `p${i}` }).icon)
    )
    expect(icons.size).toBeGreaterThan(10)
  })
})
