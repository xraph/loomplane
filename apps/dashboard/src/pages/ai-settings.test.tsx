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

it("redirects /settings/ai to / when AI is off", async () => {
  setFeatures({ ai: false })
  const off = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/settings/ai"] })
  )
  await off.load()
  expect(off.state.location.pathname).toBe("/")

  setFeatures({ ai: true })
  const on = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/settings/ai"] })
  )
  await on.load()
  expect(on.state.location.pathname).toBe("/settings/ai")
})

it("sends the old /ai/settings address to /settings/ai", async () => {
  setFeatures({ ai: true })
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/ai/settings"] })
  )
  await router.load()
  expect(router.state.location.pathname).toBe("/settings/ai")
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
  // It names no model the registry doesn't gate.
  expect(
    within(section).getByText(
      /Gated or private Hugging Face models download with it\./
    )
  ).toBeInTheDocument()
  expect(within(section).queryByText(/Llama|Gemma/)).toBeNull()

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

function agent(over: Record<string, unknown> = {}) {
  return {
    org_id: "org",
    enabled: false,
    provider_key: "",
    model: "",
    knowledge: false,
    nexus_tenant_id: "",
    revision: 3,
    updated_at: "2026-10-20T10:00:00Z",
    server: {
      provider: "anthropic",
      model: "claude-sonnet-5-5",
      key_set: true,
    },
    effective: {
      provider: "anthropic",
      model: "claude-sonnet-5-5",
      summary_model: "claude-haiku-5-5",
    },
    budget: { limit_usd: "0.00", spent_usd: "1.25", exhausted: false },
    knowledge_status: { configured: true, available: true },
    available: true,
    ...over,
  }
}

it("saves a workspace key without ever showing it again", async () => {
  let current = agent()
  const writes = stub({
    "GET /api/v1/ai/settings": () => Response.json(view()),
    "GET /api/v1/ai/settings/agent": () => Response.json(current),
    "PUT /api/v1/ai/settings/provider-keys/anthropic": () =>
      Response.json({
        id: "pk_1",
        org_id: "org",
        name: "anthropic",
        type: "anthropic",
        projects: [],
        revision: 1,
        created_by: "u",
        updated_by: "u",
        created_at: "2026-10-20T10:00:00Z",
        updated_at: "2026-10-20T10:00:00Z",
      }),
    "PATCH /api/v1/ai/settings/agent": () => {
      current = agent({
        provider_key: "anthropic",
        revision: 4,
        key: { name: "anthropic", type: "anthropic", hint: "wxyz" },
      })
      return Response.json(current)
    },
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", { name: "AI agent" })
  // The section holds its place while the agent's settings load.
  expect(await within(section).findByText("Server default")).toBeInTheDocument()
  expect(
    within(section).getByText("Anthropic, claude-sonnet-5-5")
  ).toBeInTheDocument()
  await userEvent.click(within(section).getByLabelText("Anthropic"))
  const field = within(section).getByLabelText("API key")
  expect(field).toHaveAttribute("type", "password")
  await userEvent.type(field, "sk-ant-secret-wxyz")
  await userEvent.click(
    within(section).getByRole("button", { name: "Save provider" })
  )

  // The form starts over from what was saved, so it's a new element.
  expect(await screen.findByText("Key set, ends in wxyz")).toBeInTheDocument()
  expect(writes.map((w) => [w.method, w.path, w.body]).slice(0, 2)).toEqual([
    [
      "PUT",
      "/api/v1/ai/settings/provider-keys/anthropic",
      { type: "anthropic", fields: { api_key: "sk-ant-secret-wxyz" } },
    ],
    [
      "PATCH",
      "/api/v1/ai/settings/agent",
      { provider_key: "anthropic", model: "claude-sonnet-5-5", revision: 3 },
    ],
  ])
  expect(document.body.textContent).not.toContain("sk-ant-secret")
  for (const input of document.querySelectorAll("input[type=password]"))
    expect((input as HTMLInputElement).value).toBe("")
})

it("needs a budget before turning the agent on", async () => {
  stub({
    "GET /api/v1/ai/settings": () => Response.json(view()),
    "GET /api/v1/ai/settings/agent": () => Response.json(agent()),
    "PATCH /api/v1/ai/settings/agent": () =>
      Response.json(
        {
          message:
            "invalid agent setting: give the agent a monthly budget first",
        },
        { status: 422 }
      ),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", { name: "AI agent" })
  await userEvent.click(
    await within(section).findByRole("switch", { name: "Turn the agent on" })
  )
  const budget = within(section).getByLabelText("Monthly budget (USD)")
  const alert = await within(section).findByRole("alert")
  expect(alert).toHaveTextContent("give the agent a monthly budget first")
  // It sits with the budget field, not in a banner on top.
  expect(budget.closest("[data-slot=field]")).toContainElement(alert)
})

it("explains why knowledge is off", async () => {
  stub({
    "GET /api/v1/ai/settings": () => Response.json(view()),
    "GET /api/v1/ai/settings/agent": () =>
      Response.json(
        agent({
          knowledge_status: {
            configured: false,
            available: false,
            reason: "This server has no embedding model set.",
          },
        })
      ),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", { name: "AI agent" })
  expect(
    await within(section).findByRole("switch", {
      name: "Search the workspace's documents",
    })
  ).toHaveAttribute("aria-disabled", "true")
  expect(
    within(section).getByText("This server has no embedding model set.")
  ).toBeInTheDocument()
})

it("shows no agent settings to someone without ai:manage", async () => {
  access.manage = false
  const writes = stub({
    "GET /api/v1/ai/settings": () => Response.json(view({ can_manage: false })),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  await screen.findByRole("heading", { name: "AI settings" })
  expect(screen.queryByRole("region", { name: "AI agent" })).toBeNull()
  expect(writes).toEqual([])
})

it("sets the workspace's monthly AI budget", async () => {
  const writes = stub({
    "GET /api/v1/ai/settings": () => Response.json(view()),
    "GET /api/v1/ai/settings/agent": () => Response.json(agent()),
    "PUT /api/v1/ai/settings/budget": () =>
      Response.json(
        view({
          monthly_budget_usd: "100.00",
          unallocated_usd: "100.00",
          revision: 2,
        })
      ),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", { name: "AI budget" })
  const field = within(section).getByLabelText("Workspace budget (USD)")
  await userEvent.type(field, "100")
  await userEvent.click(within(section).getByRole("button", { name: "Save" }))

  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "PUT",
        path: "/api/v1/ai/settings/budget",
        body: { monthly_budget_usd: "100", revision: 1 },
      },
    ])
  )
})

it("tells you what's left before the agent asks for more", async () => {
  const writes = stub({
    "GET /api/v1/ai/settings": () =>
      Response.json(
        view({ monthly_budget_usd: "50.00", unallocated_usd: "50.00" })
      ),
    "GET /api/v1/ai/settings/agent": () => Response.json(agent()),
  })
  render(
    <Wrapped>
      <AISettings />
    </Wrapped>
  )
  const section = await screen.findByRole("region", { name: "AI agent" })
  expect(
    await within(section).findByText(
      "Up to $50.00 from the workspace's AI budget."
    )
  ).toBeInTheDocument()

  const budget = within(section).getByLabelText("Monthly budget (USD)")
  await userEvent.type(budget, "80")
  await userEvent.click(
    within(section).getByRole("button", { name: "Save budget" })
  )
  const alert = await within(section).findByRole("alert")
  expect(alert).toHaveTextContent(
    "The agent can have up to $50.00 of the workspace's AI budget. Raise the workspace's budget above to give it more."
  )
  expect(budget.closest("[data-slot=field]")).toContainElement(alert)
  expect(writes).toEqual([])
})
