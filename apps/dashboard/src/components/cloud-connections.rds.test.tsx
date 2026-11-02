import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ConnectDialog } from "./cloud-connections"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: { orgId: "org" } }),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

it("keeps the private RDS recovery route in the actual AWS connection request", async () => {
  let submitted: Record<string, unknown> | undefined
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (path.includes("connection-clouds"))
        return Response.json({ clouds: [] })
      if (init?.method === "POST" && path.endsWith("/connections")) {
        submitted = JSON.parse(String(init.body)) as Record<string, unknown>
        return Response.json({
          id: "aws-account",
          name: "Recovery AWS",
          cloud: "aws",
          account: "123456789012",
          status: "ready",
          details: { region: "us-east-1" },
          missing: [],
        })
      }
      return Response.json({ clouds: [], connections: [] })
    })
  )
  const closed = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <ConnectDialog open cloud="aws" onOpenChange={closed} />
    </ClientProvider>
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText(/^Name$/i), "Recovery AWS")
  await user.type(
    screen.getByLabelText(/Access key ID/i),
    "AKIATESTTESTTESTTEST"
  )
  await user.type(
    screen.getByLabelText(/Secret access key/i),
    "test-test-test-test-secret"
  )
  await user.click(screen.getByText("Private RDS recovery (optional)"))
  for (const [label, value] of [
    ["Recovery VPC", "vpc-recovery"],
    ["Recovery subnet group", "recovery-private"],
    ["Recovery security group", "sg-copy"],
    ["Verifier security group", "sg-worker"],
  ])
    await user.type(screen.getByLabelText(label), value)
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(submitted).toBeDefined())
  expect(submitted?.settings).toEqual({
    region: "us-east-1",
    rds_recovery_vpc: "vpc-recovery",
    rds_recovery_subnet_group: "recovery-private",
    rds_recovery_security_group: "sg-copy",
    rds_recovery_verifier_group: "sg-worker",
  })
  await waitFor(() => expect(closed).toHaveBeenCalledWith(false))
})
