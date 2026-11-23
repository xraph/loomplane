import { expect, test } from "vitest"

import type { Environment } from "@/client/forge/loomplane/types"
import {
  chosenEnvironment,
  environmentSearchOf,
  projectSearchOf,
} from "@/lib/project-nav"

test("old tabs open where their content went", () => {
  expect(projectSearchOf({ tab: "cloud" })).toEqual({
    tab: "settings",
    section: "clouds",
  })
  expect(projectSearchOf({ tab: "analyses" })).toEqual({
    tab: "settings",
    section: "source",
  })
  expect(projectSearchOf({ tab: "backups" })).toEqual({ tab: "services" })
  expect(projectSearchOf({ tab: "graph" })).toEqual({})
  expect(projectSearchOf({ tab: "overview" })).toEqual({})
  expect(projectSearchOf({ tab: 3 })).toEqual({})
})

test("a section only counts on Settings, and only a known one", () => {
  expect(projectSearchOf({ tab: "settings", section: "manifest" })).toEqual({
    tab: "settings",
    section: "manifest",
  })
  expect(projectSearchOf({ tab: "settings", section: "danger" })).toEqual({
    tab: "settings",
  })
  expect(projectSearchOf({ tab: "variables", section: "manifest" })).toEqual({
    tab: "variables",
  })
})

test("an environment's name is a short, non-empty string", () => {
  expect(environmentSearchOf("staging")).toBe("staging")
  expect(environmentSearchOf("")).toBeUndefined()
  expect(environmentSearchOf("x".repeat(65))).toBeUndefined()
  expect(environmentSearchOf({})).toBeUndefined()
})

test("the environment shown is the one named, else production, else the first", () => {
  const list = [
    { id: "1", name: "preview", kind: "preview" },
    { id: "2", name: "prod", kind: "production" },
    { id: "3", name: "staging", kind: "staging" },
  ] as Environment[]
  expect(chosenEnvironment(list, "staging")?.name).toBe("staging")
  expect(chosenEnvironment(list, "gone")?.name).toBe("prod")
  expect(chosenEnvironment(list)?.name).toBe("prod")
  expect(
    chosenEnvironment(list.filter((e) => e.kind !== "production"))?.name
  ).toBe("preview")
  expect(chosenEnvironment([])).toBeUndefined()
})
