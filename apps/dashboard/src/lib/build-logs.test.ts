import { expect, it } from "vitest"
import type { Log } from "@/client/forge/loomplane/types"
import { mergeBuildLogs } from "@/lib/build-logs"

it("retains distinct repeated output, deduplicates replay IDs and bounds visible history", () => {
  const line = (id: string) =>
    ({ id, message: "same", time: "2026-10-14T00:00:00Z" }) as Log
  expect(
    mergeBuildLogs([line("org:1")], [line("org:1"), line("org:2")])
  ).toHaveLength(2)
  expect(
    mergeBuildLogs([line("org:1")], [line("org:2"), line("org:3")], 2).map(
      (l) => l.id
    )
  ).toEqual(["org:2", "org:3"])
})
