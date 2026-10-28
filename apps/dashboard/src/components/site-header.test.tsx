import { expect, it } from "vitest"

import type { Run, RunStatus } from "@/lib/runs"
import { waitingOnYou } from "./site-header"

const run = (status: RunStatus) => ({ status }) as Run

it("counts paused deploys as waiting on you, like plans waiting for approval", () => {
  expect(waitingOnYou([run("waiting"), run("paused"), run("running")])).toBe(2)
})
