import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory } from "@tanstack/react-router"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setFeatures } from "@/lib/features"
import { createDashboardRouter } from "@/router"
import { AISettings } from "./ai-settings"

const access = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    can: (permission: string) =>
      permission === "ai:manage" ? access.manage : true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
// The router test below needs only the route's guard, not the shell.
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => null }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))

type Write = { method: string; path: string; body: unknown }

const token = {
  account: "rex",
  hint: "wxyz",
  revision: 2,
  set_at: "2026-10-20T10:00:00Z",
}

function view(over: Record<string, unknown> = {}) {
  return {
    allocated_usd: "0.00",
    allocations: [],
    can_manage: true,
    embeddings: { mode: "off", model: "", dimensions: 0 },
    monthly_budget_usd: "0.00",
    org_id: "org",
    provider_keys: [],
    revision: 1,
    unallocated_usd: "0.00",
    updated_at: "2026-10-20T10:00:00Z",
    ...over,
  }
}

function stub(routes: Record<string, (body: unknown) => Response>): Write[] {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") writes.push({ method, path, body })
      const route = routes[`${method} ${path}`]
      if (route) return route(body)
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  return writes
}

function Wrapped({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

beforeEach(() => {
  // jsdom has no scrolling; the router restores it in the browser.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.manage = true
  setFeatures({ ai: false })
})

it("redirects /ai/settings to / when AI is off", async () => {
  setFeatures({ ai: false })
  const off = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/ai/settings"] })
  )
  await off.load()
  expect(off.state.location.pathname).toBe("/")

  setFeatures({ ai: true })
  const on = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/ai/settings"] })
  )
  await on.load()
  expect(on.state.location.pathname).toBe("/ai/settings")
})

it("keeps the Hugging Face token write-only", async () => {
  let current: typeof token | undefined
  const writes = stub({
    "GET /api/v1/ai/settings": () =>
      Response.json(view(current ? { huggingface: current } : {})),
    "PUT /api/v1/ai/settings/huggingface-token": () => {
      current = token
      return Response.json(token)
    },
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  expect(
    await screen.findByRole("heading", { name: "AI settings" })
  ).toBeInTheDocument()
  const section = screen.getByRole("region", { name: "Hugging Face token" })
  expect(within(section).getByText("No token yet")).toBeInTheDocument()

  const field = within(section).getByLabelText("Token")
  expect(field).toHaveAttribute("type", "password")
  expect(field).toHaveAttribute("placeholder", "Paste a token with read access")
  await userEvent.type(field, "hf_secretvalue")
  await userEvent.click(within(section).getByRole("button", { name: "Save" }))

  expect(
    await within(section).findByText(
      "Saved. Loomplane never shows this token again."
    )
  ).toBeInTheDocument()
  expect(writes).toEqual([
    {
      method: "PUT",
      path: "/api/v1/ai/settings/huggingface-token",
      body: { token: "hf_secretvalue" },
    },
  ])
  // The field empties and only the hint, account and set date come back.
  expect(within(section).getByLabelText("Token")).toHaveValue("")
  expect(await within(section).findByText("•••• wxyz")).toBeInTheDocument()
  expect(within(section).getByText("rex")).toBeInTheDocument()
  expect(document.body.textContent).not.toContain("hf_secretvalue")
  for (const input of document.querySelectorAll("input"))
    expect(input.value).toBe("")
})

it("hides token edits from members without ai:manage", async () => {
  access.manage = false
  stub({
    "GET /api/v1/ai/settings": () =>
      Response.json(view({ can_manage: false, huggingface: token })),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", {
    name: "Hugging Face token",
  })
  expect(within(section).getByText("•••• wxyz")).toBeInTheDocument()
  expect(within(section).getByText("rex")).toBeInTheDocument()
  expect(within(section).queryByLabelText("Token")).toBeNull()
  expect(within(section).queryByRole("button")).toBeNull()
  expect(
    within(section).getByText("Only workspace admins can change the token.")
  ).toBeInTheDocument()
})

it("shows a refused delete inline", async () => {
  const refusal =
    "Gated models download with the workspace's Hugging Face token: llama in shop. Remove them first, or put a new token in its place."
  const writes = stub({
    "GET /api/v1/ai/settings": () =>
      Response.json(view({ huggingface: token })),
    "DELETE /api/v1/ai/settings/huggingface-token": () =>
      Response.json({ code: "in_use", message: refusal }, { status: 422 }),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", {
    name: "Hugging Face token",
  })
  await within(section).findByText("•••• wxyz")
  await userEvent.click(
    within(section).getByRole("button", { name: "Remove token" })
  )
  const dialog = await screen.findByRole("alertdialog")
  await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }))

  expect(await within(section).findByText(refusal)).toBeInTheDocument()
  expect(writes.map((w) => w.method)).toEqual(["DELETE"])
  // The token stays.
  expect(within(section).getByText("•••• wxyz")).toBeInTheDocument()
})

it("renders loading, denied and failed distinctly", async () => {
  let answer: () => Response = () => new Response(null)
  let release = () => {}
  const held = new Promise<void>((resolve) => (release = resolve))
  stub({
    "GET /api/v1/ai/settings": () => answer(),
  })
  vi.stubGlobal(
    "fetch",
    (
      (inner: typeof fetch) =>
      async (...args: Parameters<typeof fetch>) => {
        await held
        return inner(...args)
      }
    )(globalThis.fetch)
  )

  // Loading: a placeholder, no error and no lock.
  answer = () => Response.json(view())
  const loading = render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  expect(screen.getByTestId("ai-settings-loading")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).toBeNull()
  expect(screen.queryByText("AI settings are restricted")).toBeNull()
  release()
  await screen.findByRole("region", { name: "Hugging Face token" })
  loading.unmount()
  forgeClient.clear()

  // Denied: the lock, no retry.
  answer = () =>
    Response.json(
      { code: "forbidden", message: "you don't have ai:read" },
      { status: 403 }
    )
  const denied = render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  expect(
    await screen.findByText("AI settings are restricted")
  ).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  expect(screen.queryByTestId("ai-settings-loading")).toBeNull()
  denied.unmount()
  forgeClient.clear()

  // Failed: the reason and a retry that works.
  answer = () => new Response(null, { status: 503 })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const retry = await screen.findByRole("button", { name: "Retry" })
  expect(
    screen.getByText("Loomplane couldn't finish that. Try again in a moment.")
  ).toBeInTheDocument()
  expect(screen.queryByText("AI settings are restricted")).toBeNull()
  answer = () => Response.json(view())
  await userEvent.click(retry)
  await waitFor(() =>
    expect(
      screen.getByRole("region", { name: "Hugging Face token" })
    ).toBeInTheDocument()
  )
})
