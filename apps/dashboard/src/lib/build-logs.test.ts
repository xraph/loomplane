import { expect, it } from "vitest"
import type { Log } from "@/client/forge/loomplane/types"
import { mergeBuildLogs, readBuildLogPages } from "@/lib/build-logs"

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

it("recovers the retained tail beyond ten thousand lines with a bounded view", async () => {
  let visible: Log[] = []
  let pages = 0
  await readBuildLogPages(
    async (cursor) => {
      const start = Number(cursor || 0)
      const end = Math.min(start + 1000, 12001)
      pages++
      return {
        logs: Array.from(
          { length: end - start },
          (_, offset) =>
            ({
              id: `org:${start + offset + 1}`,
              message:
                start + offset === 12000 ? "final build error" : "build output",
              time: "2026-10-14T00:00:00Z",
            }) as Log
        ),
        next: end < 12001 ? String(end) : "",
        gap: false,
      }
    },
    (page) => {
      visible = mergeBuildLogs(visible, page.logs)
    },
    () => true
  )
  expect(pages).toBe(13)
  expect(visible).toHaveLength(2000)
  expect(visible.at(-1)?.message).toBe("final build error")
  expect(visible[0].id).toBe("org:10002")
})
