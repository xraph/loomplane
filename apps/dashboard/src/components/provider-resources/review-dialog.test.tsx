import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ResourceReviewDialog } from "./review-dialog"

vi.mock("@/lib/router", () => ({ useNavigate: () => vi.fn() }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
test("creation options come from the installed registry and checked accounts", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("projects"))
      return Response.json({ projects: [{ id: "p", name: "Orders" }] })
    if (path.endsWith("environments"))
      return Response.json({ environments: [{ name: "production" }] })
    if (path.endsWith("manifest"))
      return Response.json({
        manifest: { version: 1, services: { api: { source: "." } } },
      })
    throw new Error(path)
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        providers={[
          {
            name: "future",
            title: "Future Provider",
            cloud: "future",
            available: true,
            provisions: [{ kind: "future-db", creates: "Future database" }],
            regions: ["west"],
          } as never,
        ]}
        connections={[
          {
            id: "c",
            name: "Checked future account",
            cloud: "future",
            status: "ready",
          } as never,
        ]}
      />
    </ClientProvider>
  )
  expect(await screen.findByText("Future database")).toBeInTheDocument()
  expect(screen.getByRole("combobox", { name: "Account" })).toHaveTextContent(
    "Checked future account"
  )
  await userEvent.click(screen.getByRole("combobox", { name: "Resource kind" }))
  expect(screen.getByRole("option", { name: "future-db" })).toBeInTheDocument()
})
