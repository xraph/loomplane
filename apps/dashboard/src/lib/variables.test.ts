import { describe, expect, it } from "vitest"

import type { Variable } from "@/client/forge/loomplane/types"
import {
  keyValid,
  kindFor,
  layerLabel,
  ownLayer,
  reachLabel,
} from "@/lib/variables"

const variable = (over: Partial<Variable>): Variable => ({
  id: "var_1",
  orgId: "aorg_1",
  scope: "project",
  key: "PORT",
  value: "3000",
  secret: false,
  environment: "",
  createdAt: "2026-10-12T00:00:00Z",
  createdBy: "ausr_1",
  updatedAt: "2026-10-12T00:00:00Z",
  updatedBy: "ausr_1",
  ...over,
})

describe("ownLayer", () => {
  const web = "svc:node:apps/web"
  it.each([
    ["a project variable, looking at the project", variable({}), "", true],
    ["a project variable, looking at a service", variable({}), web, false],
    [
      "the service's variable",
      variable({ scope: "service", service: web }),
      web,
      true,
    ],
    [
      "another service's variable",
      variable({ scope: "service", service: "svc:go:api" }),
      web,
      false,
    ],
    [
      "an organization variable",
      variable({ scope: "organization" }),
      "",
      false,
    ],
  ])("%s", (_, v, service, want) => {
    expect(ownLayer(v, { service })).toBe(want)
  })
})

describe("labels", () => {
  it("names the layer and where it applies", () => {
    expect(layerLabel(variable({ scope: "organization" }))).toBe("Organization")
    expect(reachLabel(variable({}))).toBe("every environment")
    expect(reachLabel(variable({ environment: "production" }))).toBe(
      "production only"
    )
  })
})

describe("kindFor", () => {
  it("matches the API's guess from a name", () => {
    expect(kindFor("staging")).toBe("staging")
    expect(kindFor("qa-eu")).toBe("development")
  })
})

describe("keyValid", () => {
  it.each([
    ["DATABASE_URL", true],
    ["_private", true],
    ["1BAD", false],
    ["MY-KEY", false],
    ["", false],
  ])("%s is %s", (key, want) => {
    expect(keyValid(key)).toBe(want)
  })
})
