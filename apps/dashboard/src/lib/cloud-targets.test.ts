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
  const result = adoptServiceTarget(original, "web", "frontend", {
    environment: "production",
    environments: environments,
  })
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
  const result = adoptServiceTarget(manifest, "web", "frontend", {
    environment: "production",
    environments: [{ name: "production" }] as Environment[],
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
  const result = adoptServiceTarget(manifest, "web", "frontend", {
    environment: "production",
    environments: [
      { name: "production" },
      { name: "staging" },
    ] as Environment[],
  })

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

it("puts a service on a target in every environment, clearing environment placement", () => {
  const original: Manifest = {
    version: 1,
    defaults: { provider: "vercel", region: "iad1" },
    services: {
      web: {
        source: "apps/web",
        target: "conn_a",
        environments: {
          staging: { provider: "cloud-run", region: "us-central1", port: 8080 },
          preview: { cloud_tags: ["small"] },
        },
      },
      api: { source: "cmd/api" },
    },
  }
  const result = adoptServiceTarget(original, "web", "frontend")
  const settings = (env: string) =>
    placementSettings(
      result,
      {
        section: "services",
        name: "web",
        kind: "",
        entry: result.services!.web,
      },
      env
    )
  for (const env of ["production", "staging", "preview"])
    expect(settings(env)).toMatchObject({
      provider: "",
      cloudTarget: "frontend",
    })
  // What an environment set besides placement stays.
  expect(result.services!.web.environments).toEqual({
    staging: { region: "us-central1", port: 8080 },
  })
  // Other entries keep the default they inherited.
  expect(result.services!.api).toMatchObject({
    provider: "vercel",
    region: "iad1",
  })
  expect(original.services!.web.target).toBe("conn_a")
})

it("selects a target by tags in every environment", () => {
  const result = adoptServiceTarget(
    {
      version: 1,
      services: {
        web: {
          source: "web",
          cloud_target: "old",
          environments: { staging: { cloud_target: "edge" } },
        },
      },
    },
    "web",
    { tags: ["eu", "web"] }
  )
  expect(result.services!.web).toEqual({
    source: "web",
    cloud_tags: ["eu", "web"],
  })
})

it("reads a service's Cloud the way the planner does", () => {
  const m: Manifest = {
    version: 1,
    services: {
      trainer: {
        source: "trainer",
        cloud: "GPU Lab",
        cloud_target: "a100",
        environments: {
          preview: { cloud_target: "eu-run" },
          staging: { cloud: "GPU Lab EU" },
        },
      },
    },
  }
  const settings = (env: string) =>
    placementSettings(
      m,
      {
        section: "services",
        name: "trainer",
        kind: "",
        entry: m.services!.trainer,
      },
      env
    )
  expect(settings("production")).toMatchObject({
    cloud: "GPU Lab",
    cloudTarget: "a100",
  })
  // A target named without a Cloud is in the default Cloud.
  expect(settings("preview").cloud).toBeUndefined()
  expect(settings("preview").cloudTarget).toBe("eu-run")
  expect(settings("staging")).toMatchObject({
    cloud: "GPU Lab EU",
    cloudTarget: "a100",
  })
})

it("puts a service on another Cloud's target, everywhere or in one environment", () => {
  const m: Manifest = {
    version: 1,
    services: { trainer: { source: "trainer", cloud_target: "eu-run" } },
  }
  expect(
    adoptServiceTarget(m, "trainer", "a100", undefined, "GPU Lab").services!
      .trainer
  ).toEqual({ source: "trainer", cloud: "GPU Lab", cloud_target: "a100" })
  const environments = [
    { name: "production" },
    { name: "preview" },
  ] as Environment[]
  const one = adoptServiceTarget(
    m,
    "trainer",
    "a100",
    { environment: "production", environments },
    "GPU Lab"
  ).services!.trainer
  expect(one.environments.production).toEqual({
    cloud: "GPU Lab",
    cloud_target: "a100",
  })
  // The other environment keeps the default Cloud's target.
  expect(one.environments.preview).toEqual({ cloud_target: "eu-run" })
})
