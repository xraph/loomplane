import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { EdgeCacheDialog } from "./edge-cache-dialog"

const zero = "0001-01-01T00:00:00Z"
const initial = () => ({
  route_id: "route",
  org_id: "org",
  project_id: "shop",
  revision: 0,
  status: "disabled",
  message: "",
  kind: "",
  requested_at: zero,
  applied_at: zero,
  last_purge_at: zero,
  policy: {
    enabled: false,
    invalidate_on_deploy: false,
    rules: [] as unknown[],
  },
})

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function setup(
  options: {
    enabled?: boolean
    writable?: boolean
    denied?: boolean
    failed?: boolean
  } = {}
) {
  const state = initial()
  if (options.enabled) {
    state.policy.enabled = true
    state.status = "ready"
    state.revision = 2
  }
  if (options.failed) {
    state.status = "failed"
    state.message = "Cache Rules permission denied"
  }
  const writes: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | Request, init?: RequestInit) => {
      const path = typeof input === "string" ? input : input.url
      if (options.denied)
        return new Response(JSON.stringify({ message: "Access denied" }), {
          status: 403,
          headers: { "Content-Type": "application/json" },
        })
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body))
        writes.push({ path, body })
        state.revision++
        state.status = "pending"
        if (body.policy) state.policy = body.policy
      }
      return new Response(JSON.stringify(state), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    })
  )
  render(
    <ClientProvider client={forgeClient}>
      <EdgeCacheDialog
        projectId="shop"
        routeId="route"
        hostname="api.example.com"
        writable={options.writable ?? true}
        onClose={() => undefined}
      />
    </ClientProvider>
  )
  return { writes, state }
}

test("saves public TTL rules and deployment invalidation through the wire codec", async () => {
  const user = userEvent.setup()
  const { writes } = setup()
  await user.click(
    await screen.findByRole("switch", { name: "Enable global CDN" })
  )
  await user.click(
    screen.getByRole("switch", { name: /successful deployments/ })
  )
  await user.click(screen.getByRole("button", { name: "Add rule" }))
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Behavior 1" }),
    "public"
  )
  await user.clear(screen.getByRole("spinbutton", { name: "Edge TTL 1" }))
  await user.type(
    screen.getByRole("spinbutton", { name: "Edge TTL 1" }),
    "7200"
  )
  await user.click(screen.getByRole("button", { name: "Save CDN settings" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].body).toEqual({
    revision: 0,
    policy: {
      enabled: true,
      invalidate_on_deploy: true,
      rules: [
        {
          path_prefix: "/assets/",
          mode: "public",
          edge_ttl: 7200,
          browser_ttl: 0,
        },
      ],
    },
  })
  expect(await screen.findByText("Applying…")).toBeVisible()
  expect(
    screen.getByRole("button", { name: "Save CDN settings" })
  ).toBeDisabled()
})

test("purges exact URLs without changing settings", async () => {
  const user = userEvent.setup()
  const { writes } = setup({ enabled: true })
  await user.type(
    await screen.findByRole("textbox", { name: "URLs to purge" }),
    "https://api.example.com/logo.svg"
  )
  await user.click(screen.getByRole("button", { name: "Purge URLs" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    path: "/api/v1/projects/shop/domain-routes/route/edge/purge",
    body: { revision: 2, urls: ["https://api.example.com/logo.svg"] },
  })
})

test("read-only viewers can inspect settings without mutation controls", async () => {
  setup({ enabled: true, writable: false })
  expect(
    await screen.findByRole("switch", { name: "Enable global CDN" })
  ).toHaveAttribute("aria-disabled", "true")
  expect(
    screen.queryByRole("button", { name: "Save CDN settings" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Purge hostname" })
  ).not.toBeInTheDocument()
})

test("denied access is a failed read rather than a disabled CDN", async () => {
  setup({ denied: true })
  expect(await screen.findByText(/Access denied/)).toBeVisible()
  expect(screen.queryByRole("switch")).not.toBeInTheDocument()
})

test("a failed operation offers retry and disable recovery", async () => {
  setup({ failed: true })
  expect(await screen.findByText("Cache Rules permission denied")).toBeVisible()
  expect(screen.getByRole("button", { name: "Retry operation" })).toBeEnabled()
  expect(
    screen.getByRole("button", { name: "Disable CDN and recover" })
  ).toBeEnabled()
  expect(
    screen.getByRole("button", { name: "Save CDN settings" })
  ).toBeDisabled()
})
