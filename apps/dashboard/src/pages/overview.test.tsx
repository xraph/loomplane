import { expect, it } from "vitest"

import type { Run, RunStatus } from "@/lib/runs"
import { summary } from "./overview"

const run = (id: string, status: RunStatus, environment = id): Run =>
  ({
    id,
    status,
    projectId: "proj_1",
    environment,
    phase: "",
    createdAt: "2026-10-27T10:00:00Z",
    createdBy: "user_1",
    thread: [],
  }) as unknown as Run

it("says a paused deploy is paused instead of everything being live", () => {
  expect(summary([run("a", "paused"), run("b", "succeeded")])).toBe(
    "One deploy is paused for capacity. Everything else is live."
  )
})

it("counts several paused deploys", () => {
  expect(
    summary([run("a", "paused"), run("b", "paused"), run("c", "waiting")])
  ).toBe(
    "One deploy is waiting on you. Two deploys are paused for capacity. Everything else is live."
  )
})

it("still says everything is live when nothing needs you", () => {
  expect(summary([run("a", "succeeded")])).toBe("Everything is live.")
})
