import { expect, it } from "vitest"

import { pageContextOf } from "@/lib/page-context"

const names = new Map([["proj_1", "api"]])

it("reads a project, an environment and a service", () => {
  expect(pageContextOf("/projects/proj_1", {}, names)).toEqual({
    kind: "project",
    path: "/projects/proj_1",
    label: "Project api",
    projectId: "proj_1",
  })
  expect(
    pageContextOf("/projects/proj_1", { environment: "production" }, names)
  ).toEqual({
    kind: "environment",
    path: "/projects/proj_1?environment=production",
    label: "api, production",
    projectId: "proj_1",
    environment: "production",
  })
  expect(
    pageContextOf(
      "/projects/proj_1/services/web",
      { environment: "staging", tab: "logs" },
      names
    )
  ).toEqual({
    kind: "service",
    path: "/projects/proj_1/services/web?environment=staging",
    label: "Service web in api, staging",
    projectId: "proj_1",
    service: "web",
    environment: "staging",
  })
})

it("names a project it doesn't know yet as this project", () => {
  expect(pageContextOf("/projects/proj_9/fleet", {}, names)?.label).toBe(
    "The fleet of this project"
  )
})

it("reads a deployment by its plan, and plain pages by name", () => {
  expect(pageContextOf("/deployments/plan_42", {}, names)).toEqual({
    kind: "deployment",
    path: "/deployments/plan_42",
    label: "Deployment plan_42",
    planId: "plan_42",
  })
  expect(pageContextOf("/deployments/new", {}, names)).toBeUndefined()
  expect(pageContextOf("/", {}, names)?.label).toBe("Overview")
  expect(pageContextOf("/clouds/", {}, names)?.label).toBe("Clouds")
  expect(pageContextOf("/domains/zone_1", {}, names)?.kind).toBe("domain")
})

it("has nothing to say about settings and unknown pages", () => {
  expect(pageContextOf("/settings/ai", {}, names)).toBeUndefined()
  expect(pageContextOf("/account/security", {}, names)).toBeUndefined()
})

it("keeps a label short enough for the server", () => {
  const long = "s".repeat(200)
  const c = pageContextOf(`/projects/proj_1/services/${long}`, {}, names)
  expect(c?.label.length).toBeLessThanOrEqual(120)
})
