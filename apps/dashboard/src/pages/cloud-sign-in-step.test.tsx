import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setInstance } from "@/lib/instance"
import { CloudSignInStep } from "@/pages/cloud-sign-in-step"

const navigate = vi.hoisted(() => vi.fn())
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  usePath: () => "/setup/cloud-sign-in",
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { orgId: "org", userId: "u1" },
  }),
}))

const instance = {
  setupPending: false,
  waitlist: false,
  setupTokenRequired: false,
  platformAdmin: true,
  cloudSignInOffer: true,
}

let endStatus = 204
let ended = 0
let saved = 0

beforeEach(() => {
  endStatus = 204
  ended = 0
  saved = 0
  navigate.mockClear()
  setInstance(instance)
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "DELETE" && path.includes("cloud-sign-in-offer")) {
        ended++
        return endStatus === 204
          ? new Response(null, { status: 204 })
          : Response.json(
              { message: "The offer couldn't end." },
              { status: 500 }
            )
      }
      if (init?.method === "PUT") {
        saved++
        return Response.json({ cloud: "gcp", source: "platform" })
      }
      if (path.includes("platform/cloud-sign-in/apps"))
        return Response.json({
          apps: [{ cloud: "gcp", source: "none" }],
          callback_url: "https://loomplane.example/cb",
          keyless: { enabled: true, issuer: "x", reason: "" },
        })
      return Response.json({ members: [], clouds: [] })
    })
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function renderStep() {
  return render(
    <ClientProvider client={forgeClient}>
      <CloudSignInStep />
    </ClientProvider>
  )
}

it("shows the step's copy and each cloud collapsed", async () => {
  renderStep()
  expect(
    screen.getByRole("heading", { name: "Cloud sign-in (optional)" })
  ).toBeTruthy()
  expect(
    screen.getByText(
      "Let people connect a cloud by signing in instead of pasting a token. Every organization on this instance uses these apps. You can change them later in Settings, under Cloud sign-in."
    )
  ).toBeTruthy()
  const open = await screen.findByRole("button", {
    name: "Set up Google Cloud",
  })
  expect(open.getAttribute("aria-expanded")).toBe("false")
  expect(screen.queryByLabelText("Client ID")).toBeNull()
  expect(screen.queryByText(/Keyless/)).toBeNull()
  await userEvent.setup().click(open)
  expect(await screen.findByLabelText("Client ID")).toBeTruthy()
  expect(screen.queryByRole("dialog")).toBeNull()
})

it("skips: ends the offer, records it and goes home", async () => {
  const user = userEvent.setup()
  renderStep()
  await user.click(screen.getByRole("button", { name: "Skip for now" }))
  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/" }))
  expect(ended).toBe(1)
  const { useInstance } = await import("@/lib/instance")
  expect(useInstance().cloudSignInOffer).toBe(false)
})

it("says Continue once an app is saved", async () => {
  const user = userEvent.setup()
  renderStep()
  await user.click(
    await screen.findByRole("button", { name: "Set up Google Cloud" })
  )
  await user.type(await screen.findByLabelText("Client ID"), "id")
  await user.type(screen.getByLabelText("Client secret"), "sec")
  await user.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(saved).toBe(1))
  const next = await screen.findByRole("button", { name: "Continue" })
  expect(screen.queryByRole("button", { name: "Skip for now" })).toBeNull()
  await user.click(next)
  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/" }))
  expect(ended).toBe(1)
})

it("shows the failure and stays on the step", async () => {
  endStatus = 500
  const user = userEvent.setup()
  renderStep()
  await user.click(screen.getByRole("button", { name: "Skip for now" }))
  expect(await screen.findByText("The offer couldn't end.")).toBeTruthy()
  expect(navigate).not.toHaveBeenCalled()
  const { useInstance } = await import("@/lib/instance")
  expect(useInstance().cloudSignInOffer).toBe(true)
})
