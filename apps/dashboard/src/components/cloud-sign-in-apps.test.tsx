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
import {
  CloudSignInApps,
  cloudSignInDocsUrl,
} from "@/components/cloud-sign-in-apps"

vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  usePath: () => "/cloud-sign-in",
}))
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

function stub(
  apps: App[],
  onPut?: (body: Record<string, unknown>) => void,
  onDelete?: () => void
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "PUT") {
        onPut?.(JSON.parse(String(init.body)) as Record<string, unknown>)
        return Response.json({ cloud: "gcp", source: "platform" })
      }
      if (init?.method === "DELETE") {
        onDelete?.()
        return new Response(null, { status: 204 })
      }
      if (path.includes("platform/cloud-sign-in/apps"))
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
  { cloud: "gcp", source: "config", connections: 0, organizations: 0 },
  {
    cloud: "aws",
    source: "platform",
    client_id: "aws-id",
    updated_by: "u1",
    updated_at: "2026-10-20T10:00:00Z",
  },
  { cloud: "vercel", source: "none" },
]

function renderPage() {
  return render(
    <ClientProvider client={forgeClient}>
      <CloudSignInApps variant="page" />
    </ClientProvider>
  )
}

it("shows where each cloud's sign-in app comes from", async () => {
  stub(mixed)
  renderPage()
  expect(await screen.findByText("Set by the deployment · locked")).toBeTruthy()
  expect(await screen.findByText(/Set by Ada Lovelace · /)).toBeTruthy()
  expect(screen.getByText("Not set up · people use a token")).toBeTruthy()
})

it("locks an app set in config", async () => {
  stub([{ cloud: "gcp", source: "config", connections: 0, organizations: 0 }])
  renderPage()
  await screen.findByText("Set by the deployment · locked")
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
      source: "platform",
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

it("explains the Cloudflare app registration before saving", async () => {
  stub([{ cloud: "cloudflare", source: "none" }])
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Set up" }))
  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog)
      .getByRole("link", { name: "Register a Cloudflare OAuth client" })
      .getAttribute("href")
  ).toBe(
    "https://developers.cloudflare.com/fundamentals/oauth/create-an-oauth-client/"
  )
  expect(within(dialog).getByText(/client_secret_basic/)).toBeTruthy()
  expect(within(dialog).getByText(/offline_access/)).toBeTruthy()
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

const rotated = {
  cloud: "gcp",
  source: "platform",
  client_id: "old-id",
  connections: 12,
  organizations: 4,
}
const warning =
  "12 connections in 4 organizations signed in with this app will need to sign in again."

it("asks before a new client ID replaces an app with connections", async () => {
  let body: Record<string, unknown> | undefined
  stub([rotated], (b) => {
    body = b
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  const dialog = await screen.findByRole("dialog")
  const id = within(dialog).getByLabelText("Client ID")
  await user.clear(id)
  await user.type(id, " new-id ")
  await user.type(within(dialog).getByLabelText("Client secret"), "sec")
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  const confirm = await screen.findByRole("dialog")
  expect(within(confirm).getByText(warning)).toBeTruthy()
  expect(body).toBeUndefined()
  await user.click(within(confirm).getByRole("button", { name: "Replace app" }))
  await waitFor(() =>
    expect(body).toEqual({ client_id: "new-id", client_secret: "sec" })
  )
})

it("goes back to the form when the replacement is cancelled", async () => {
  stub([rotated])
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  const dialog = await screen.findByRole("dialog")
  const id = within(dialog).getByLabelText("Client ID")
  await user.clear(id)
  await user.type(id, "new-id")
  await user.type(within(dialog).getByLabelText("Client secret"), "sec")
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await user.click(await screen.findByRole("button", { name: "Cancel" }))
  expect(
    ((await screen.findByLabelText("Client ID")) as HTMLInputElement).value
  ).toBe("new-id")
})

it("rotating only the secret asks nothing", async () => {
  let body: Record<string, unknown> | undefined
  stub([rotated], (b) => {
    body = b
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Client secret"), "fresh")
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(body).toEqual({ client_id: "old-id", client_secret: "fresh" })
  )
  expect(screen.queryByText(warning)).toBeNull()
})

it("asks before removing an app with connections", async () => {
  let removed = false
  stub([rotated], undefined, () => {
    removed = true
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  const dialog = await screen.findByRole("dialog")
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  const confirm = await screen.findByRole("dialog")
  expect(within(confirm).getByText(warning)).toBeTruthy()
  expect(removed).toBe(false)
  await user.click(within(confirm).getByRole("button", { name: "Remove app" }))
  await waitFor(() => expect(removed).toBe(true))
})

it("removes an app nobody signed in with, without asking", async () => {
  let removed = false
  stub([{ ...rotated, connections: 0, organizations: 0 }], undefined, () => {
    removed = true
  })
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  const dialog = await screen.findByRole("dialog")
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))
  await waitFor(() => expect(removed).toBe(true))
})

it("uses singular forms for one connection", async () => {
  stub([{ ...rotated, connections: 1, organizations: 1 }])
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole("button", { name: "Edit" }))
  await user.click(await screen.findByRole("button", { name: "Remove" }))
  expect(
    await screen.findByText(
      "1 connection in 1 organization signed in with this app needs to sign in again."
    )
  ).toBeTruthy()
})

it("links each cloud to its guide", async () => {
  stub(mixed)
  renderPage()
  await screen.findByText(callbackUrl)
  for (const cloud of ["gcp", "aws", "vercel"]) {
    const link = document.querySelector(
      `a[href="${cloudSignInDocsUrl(cloud)}"]`
    )
    expect(link).toBeTruthy()
  }
})

it("maps clouds to documentation anchors", () => {
  const base = "https://loomplane.dev/docs/clouds/cloud-sign-in#"
  expect(cloudSignInDocsUrl("digitalocean")).toBe(base + "digitalocean")
  expect(cloudSignInDocsUrl("gcp")).toBe(base + "google-cloud")
  expect(cloudSignInDocsUrl("azure")).toBe(base + "azure")
  expect(cloudSignInDocsUrl("cloudflare")).toBe(base + "cloudflare")
  expect(cloudSignInDocsUrl("vercel")).toBe(base + "vercel")
})
