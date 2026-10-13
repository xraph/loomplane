import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DeployDialog } from "@/components/deploy-dialog"

vi.mock("@/lib/router", () => ({ useNavigate: () => vi.fn() }))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("shows a failed project read and recovers on retry", async () => {
  let failed = true
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(
        JSON.stringify(
          failed ? { error: "Projects unavailable" } : { projects: [] }
        ),
        {
          status: failed ? 503 : 200,
          headers: { "Content-Type": "application/json" },
        }
      )
  )
  render(
    <ClientProvider client={forgeClient}>
      <DeployDialog onOpenChange={() => {}} preset={{}} />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("alert", {}, { timeout: 5000 })
  ).toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No projects to deploy")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})
