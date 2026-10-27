import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { CloudSignIn } from "@/pages/cloud-sign-in"

vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  usePath: () => "/cloud-sign-in",
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { orgId: "org", userId: "u1" },
  }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const callbackUrl = "https://loomplane.example/api/v1/cloud-sign-in/callback"

type App = Record<string, unknown>

let keyless: Record<string, unknown> = {
  enabled: true,
  issuer: "https://loomplane.example",
  reason: "",
}

function stub(apps: App[], onPut?: (body: Record<string, unknown>) => void) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "PUT") {
        onPut?.(JSON.parse(String(init.body)) as Record<string, unknown>)
        return Response.json({ cloud: "gcp", source: "organization" })
      }
      if (path.includes("cloud-sign-in/apps"))
        return Response.json({ apps, callback_url: callbackUrl, keyless })
      if (path.includes("members"))
        return Response.json({
          members: [
            { user_id: "u1", name: "Ada Lovelace", email: "ada@example.com" },
          ],
        })
      return Response.json({ clouds: [] })
    })
  )
}

const mixed = [
  { cloud: "gcp", source: "config" },
  {
    cloud: "aws",
    source: "organization",
    client_id: "aws-id",
    updated_by: "u1",
    updated_at: "2026-10-20T10:00:00Z",
  },
  { cloud: "vercel", source: "none" },
]

function renderPage() {
  return render(
    <ClientProvider client={forgeClient}>
      <CloudSignIn />
    </ClientProvider>
  )
}

it("shows where each cloud's sign-in app comes from", async () => {
  stub(mixed)
  renderPage()
  expect(await screen.findByText("Set in config · locked")).toBeTruthy()
  expect(await screen.findByText(/Set here by Ada Lovelace · /)).toBeTruthy()
  expect(screen.getByText("Not set up · people use a token")).toBeTruthy()
})

it("locks an app set in config", async () => {
  stub([{ cloud: "gcp", source: "config" }])
  renderPage()
  await screen.findByText("Set in config · locked")
  expect(
    (screen.getByRole("button", { name: "View" }) as HTMLButtonElement).disabled
  ).toBe(true)
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Set up" })).toBeNull()
})

it("saves a client ID and secret and never shows the secret again", async () => {
  let body: Record<string, unknown> | undefined
  stub([{ cloud: "gcp", source: "none" }], (b) => {
    body = b
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Set up" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Client ID"), "the-id")
  await user.type(
    within(dialog).getByLabelText("Client secret"),
    "s3cret-value"
  )
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(body).toEqual({ client_id: "the-id", client_secret: "s3cret-value" })
  )
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(document.body.innerHTML).not.toContain("s3cret-value")
  expect(screen.queryByDisplayValue("s3cret-value")).toBeNull()

  // Opening it again starts with an empty secret.
  await user.click(await screen.findByRole("button", { name: "Set up" }))
  const again = await screen.findByRole("dialog")
  expect(
    (within(again).getByLabelText("Client secret") as HTMLInputElement).value
  ).toBe("")
})

it("shows the callback URL to register", async () => {
  stub(mixed)
  renderPage()
  expect(await screen.findByText(callbackUrl)).toBeTruthy()
  expect(screen.getByRole("button", { name: "Copy callback URL" })).toBeTruthy()
})

it("asks for the Vercel integration slug", async () => {
  let body: Record<string, unknown> | undefined
  stub([{ cloud: "vercel", source: "none" }], (b) => {
    body = b
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Set up" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Client ID"), "id")
  await user.type(within(dialog).getByLabelText("Client secret"), "sec")
  await user.type(within(dialog).getByLabelText("Integration slug"), " my-app ")
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(body).toEqual({
      client_id: "id",
      client_secret: "sec",
      slug: "my-app",
    })
  )
})

it("shows a sign-in app's problem", async () => {
  stub([
    {
      cloud: "vercel",
      source: "organization",
      client_id: "v",
      problem: "The integration slug is missing.",
    },
  ])
  renderPage()
  expect(
    await screen.findByText("The integration slug is missing.")
  ).toBeTruthy()
})

it("warns about Google testing mode", async () => {
  stub([{ cloud: "gcp", source: "none" }])
  renderPage()
  expect(
    await screen.findByText(
      "While your Google OAuth app is in testing, Google ends sign-ins after 7 days. Publish the app to keep them."
    )
  ).toBeTruthy()
})

it("shows keyless on, with the issuer", async () => {
  keyless = { enabled: true, issuer: "https://loomplane.example", reason: "" }
  stub(mixed)
  renderPage()
  expect(
    await screen.findByText("Keyless: on · https://loomplane.example")
  ).toBeTruthy()
})

it("shows keyless off and why", async () => {
  keyless = {
    enabled: false,
    issuer: "http://loomplane.example",
    reason: "public_url isn't HTTPS",
  }
  stub(mixed)
  renderPage()
  expect(
    await screen.findByText("Keyless: off · public_url isn't HTTPS")
  ).toBeTruthy()
})

it("shows the warning when keyless is on in development", async () => {
  keyless = {
    enabled: true,
    issuer: "http://localhost:8080",
    reason: "the issuer is on a loopback address, which no cloud can reach",
  }
  stub(mixed)
  renderPage()
  expect(
    await screen.findByText("Keyless: on · http://localhost:8080")
  ).toBeTruthy()
  expect(screen.getByText(/loopback address/)).toBeTruthy()
})
