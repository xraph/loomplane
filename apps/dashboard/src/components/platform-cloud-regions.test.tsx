import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { PlatformCloudRegions } from "@/components/platform-cloud-regions"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

type Call = { method: string; path: string; body: Record<string, unknown> }
type Row = {
  id: string
  name: string
  display_name: string
  status: string
  revision: number
}

/**
 * stub plays the region routes like the server: a status other than
 * "active" or "closed" is refused with 422, and a stale revision with 409.
 */
function stub(regions: Row[], conflict = false) {
  const calls: Call[] = []
  let lists = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      const method = init?.method ?? "GET"
      if (method === "GET") {
        lists++
        return Response.json({ regions })
      }
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<
        string,
        unknown
      >
      calls.push({ method, path, body })
      if (method === "POST") {
        const r = {
          id: `r${regions.length + 1}`,
          name: String(body.name),
          display_name: String(body.display_name),
          status: "active",
          revision: 1,
        }
        regions.push(r)
        return Response.json(r)
      }
      const r = regions.find((x) => path.endsWith(`/regions/${x.id}`))!
      if (conflict)
        return Response.json(
          { message: "platformcloud: the region changed" },
          { status: 409 }
        )
      if (body.status !== "active" && body.status !== "closed")
        return Response.json(
          { message: "platformcloud: a region is active or closed" },
          { status: 422 }
        )
      r.status = String(body.status)
      r.revision++
      return Response.json(r)
    })
  )
  return { calls, lists: () => lists }
}

function renderRegions() {
  return render(
    <ClientProvider client={forgeClient}>
      <PlatformCloudRegions />
    </ClientProvider>
  )
}

it("adds, closes and reopens a region", async () => {
  const { calls } = stub([])
  const user = userEvent.setup()
  renderRegions()
  await user.type(await screen.findByLabelText("Region name"), "eu")
  await user.type(screen.getByLabelText("Region display name"), "Europe")
  await user.click(screen.getByRole("button", { name: "Add region" }))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].body).toMatchObject({ name: "eu", display_name: "Europe" })

  await user.click(await screen.findByRole("button", { name: "Close Europe" }))
  await waitFor(() => expect(calls).toHaveLength(2))
  expect(calls[1].body).toMatchObject({ status: "closed", revision: 1 })
  expect(await screen.findByText("Closed")).toBeTruthy()

  await user.click(await screen.findByRole("button", { name: "Open Europe" }))
  await waitFor(() => expect(calls).toHaveLength(3))
  expect(calls[2].body).toMatchObject({ status: "active", revision: 2 })
  expect(await screen.findByText("Open")).toBeTruthy()
  expect(screen.queryByRole("alert")).toBeNull()
  expect(screen.queryByText("active")).toBeNull()
})

it("reloads the list after a conflict so the next try has the new revision", async () => {
  const { lists } = stub(
    [
      {
        id: "r1",
        name: "eu",
        display_name: "Europe",
        status: "active",
        revision: 3,
      },
    ],
    true
  )
  const user = userEvent.setup()
  renderRegions()
  await user.click(await screen.findByRole("button", { name: "Close Europe" }))
  expect(await screen.findByText("The region changed.")).toBeTruthy()
  await waitFor(() => expect(lists()).toBeGreaterThan(1))
})
