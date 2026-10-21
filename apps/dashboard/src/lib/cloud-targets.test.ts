import { expect, it } from "vitest"
import type { Environment, Manifest } from "@/client/forge/loomplane/types"
import { adoptServiceTarget } from "./cloud-targets"
import { placementSettings } from "./placement"

it("adopts one environment while retaining effective defaults, unrelated entries and other environments", () => {
  const original: Manifest = {
    version: 1,
    defaults: { provider: "vercel", region: "iad1" },
    services: {
      web: {
        source: "apps/web",
        target: "conn_a",
        environments: {
          staging: { provider: "cloud-run", region: "us-central1", port: 8080 },
        },
      },
      api: { source: "cmd/api" },
    },
    resources: { db: { kind: "postgres" } },
    links: { private: { between: ["gcp", "vercel"] } },
  }
  const environments = [
    { id: "p", name: "production" },
    { id: "s", name: "staging" },
  ] as Environment[]
  const result = adoptServiceTarget(
    original,
    environments,
    "production",
    "web",
    "frontend"
  )
  const settings = (m: Manifest, env: string) =>
    placementSettings(
      m,
      { section: "services", name: "web", kind: "", entry: m.services!.web },
      env
    )
  expect(settings(result, "production")).toMatchObject({
    provider: "",
    cloudTarget: "frontend",
  })
  expect(settings(result, "production").target).toBeUndefined()
  expect(settings(result, "staging")).toMatchObject({
    provider: "cloud-run",
    region: "us-central1",
  })
  expect(result.services!.web.environments.staging.port).toBe(8080)
  expect(result.services!.api).toMatchObject({
    provider: "vercel",
    region: "iad1",
  })
  expect(result.resources!.db).toMatchObject({
    provider: "vercel",
    region: "iad1",
  })
  expect(result.links).toEqual(original.links)
  expect(original.defaults?.provider).toBe("vercel")
  expect(original.services!.web.cloud_target).toBeUndefined()
})

it("preserves placement in saved overrides before an environment is registered", () => {
  const manifest: Manifest = {
    version: 1,
    defaults: { provider: "vercel", region: "iad1" },
    services: {
      web: { source: "web", environments: { future: { port: 9000 } } },
    },
  }
  const result = adoptServiceTarget(
    manifest,
    [{ name: "production" }] as Environment[],
    "production",
    "web",
    "frontend"
  )
  expect(
    placementSettings(
      result,
      {
        section: "services",
        name: "web",
        kind: "",
        entry: result.services!.web,
      },
      "future"
    )
  ).toMatchObject({ provider: "vercel", region: "iad1" })
  expect(result.services!.web.environments.future.port).toBe(9000)
})

it("retains inherited endpoints when another environment adopts a target", () => {
  const manifest: Manifest = {
    version: 1,
    services: {
      web: {
        source: "web",
        provider: "server",
        target: "conn",
        endpoint: {
          hostname: "staging.example",
          host_port: 8080,
          ingress_class: "nginx",
          tls_secret: "web-tls",
        },
      },
    },
  }
  const result = adoptServiceTarget(
    manifest,
    [{ name: "production" }, { name: "staging" }] as Environment[],
    "production",
    "web",
    "frontend"
  )
  expect(result.services!.web.environments.staging.endpoint).toEqual({
    hostname: "staging.example",
    host_port: 8080,
    ingress_class: "nginx",
    tls_secret: "web-tls",
  })
  expect(
    placementSettings(
      result,
      {
        section: "services",
        name: "web",
        kind: "",
        entry: result.services!.web,
      },
      "staging"
    ).endpoint
  ).toEqual(
    placementSettings(
      manifest,
      {
        section: "services",
        name: "web",
        kind: "",
        entry: manifest.services!.web,
      },
      "staging"
    ).endpoint
  )
})
