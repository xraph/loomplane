import { expect, it } from "vitest"

import type {
  Environment,
  Manifest,
  Target,
} from "@/client/forge/loomplane/types"

import { adoptServiceTarget, matchCloudTags } from "./cloud-targets"
import { placementSettings, type PlacementRow } from "./placement"

const service = (m: Manifest, name: string): PlacementRow => ({
  section: "services",
  name,
  kind: "",
  entry: m.services![name],
})

it("follows the planner's precedence for cloud_tags", () => {
  const m: Manifest = {
    version: 1,
    defaults: { cloudTags: ["eu"] },
    services: {
      web: {
        source: "apps/web",
        cloud_tags: ["frontend", "eu"],
        environments: { staging: { cloud_target: "preview" } },
      },
      api: {
        source: "cmd/api",
        cloud_target: "api",
        environments: { staging: { cloud_tags: ["gpu"] } },
      },
      plain: { source: "cmd/plain" },
      direct: { source: "cmd/direct", provider: "vercel" },
    },
  }
  const at = (name: string, env: string) =>
    placementSettings(m, service(m, name), env)
  expect(at("web", "production")).toMatchObject({
    cloudTags: ["frontend", "eu"],
  })
  expect(at("web", "production").cloudTarget).toBeUndefined()
  expect(at("web", "staging")).toMatchObject({ cloudTarget: "preview" })
  expect(at("web", "staging").cloudTags).toBeUndefined()
  expect(at("api", "staging")).toMatchObject({ cloudTags: ["gpu"] })
  expect(at("api", "staging").cloudTarget).toBeUndefined()
  expect(at("plain", "production")).toMatchObject({ cloudTags: ["eu"] })
  expect(at("direct", "production").cloudTags).toBeUndefined()
})

it("matches tags to exactly one target, like the planner", () => {
  const targets = [
    { id: "a", name: "eu-web", tags: ["eu", "web"] },
    { id: "b", name: "us-web", tags: ["us", "web"] },
    { id: "c", name: "plain" },
  ] as Target[]
  expect(matchCloudTags(targets, ["eu"])).toMatchObject({
    kind: "one",
    target: { id: "a" },
  })
  expect(matchCloudTags(targets, ["web", "us"])).toMatchObject({
    kind: "one",
    target: { id: "b" },
  })
  expect(matchCloudTags(targets, ["gpu"])).toEqual({ kind: "none" })
  const many = matchCloudTags(targets, ["web"])
  expect(many.kind).toBe("many")
  expect(many.kind === "many" && many.targets.map((t) => t.id)).toEqual([
    "a",
    "b",
  ])
})

it("selects one environment's target by tags and pins the others", () => {
  const environments = [
    { id: "p", name: "production" },
    { id: "s", name: "staging" },
  ] as Environment[]
  const m: Manifest = {
    version: 1,
    services: {
      web: {
        source: "apps/web",
        cloud_target: "frontend",
        environments: { staging: { cloud_tags: ["preview"] } },
      },
    },
  }
  const next = adoptServiceTarget(m, environments, "production", "web", {
    tags: ["eu", "web"],
  })
  const web = next.services!.web
  expect(web.cloud_target).toBeUndefined()
  expect(web.environments.production).toEqual({ cloud_tags: ["eu", "web"] })
  expect(web.environments.staging).toEqual({ cloud_tags: ["preview"] })

  // Switching back to a name drops the tags in that environment only.
  const named = adoptServiceTarget(next, environments, "staging", "web", "edge")
  expect(named.services!.web.environments.staging).toEqual({
    cloud_target: "edge",
  })
  expect(named.services!.web.environments.production).toEqual({
    cloud_tags: ["eu", "web"],
  })
})
