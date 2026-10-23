import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { NativeRecovery } from "@/components/native-recovery"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true, session: { orgId: "org" } }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture({
  fail = false,
  empty = false,
  cloudSQL = false,
  expiresAt,
  dropExpiryOnRefresh = false,
}: {
  fail?: boolean
  empty?: boolean
  cloudSQL?: boolean
  expiresAt?: string
  dropExpiryOnRefresh?: boolean
} = {}) {
  let refreshes = 0
  const source = {
    id: "source",
    org_id: "org",
    project_id: "p",
    environment_id: "prod",
    name: "database",
    provider: cloudSQL ? "cloud-sql" : "rds",
    connection_id: "account",
    physical_id: cloudSQL ? "cloud-sql:original" : "db-original",
    region: "us-east-1",
    captured_at: "2026-10-21T00:00:00Z",
    state: "ready",
    complete: false,
    observed_at: "2026-10-21T00:00:00Z",
  }
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === "POST") {
        refreshes++
        if (fail)
          return new Response(JSON.stringify({ error: "Refresh denied" }), {
            status: 503,
          })
      }
      const body = url.includes("/projects/")
        ? { sources: empty ? [] : [source] }
        : {
            source: {
              ...source,
              state: refreshes && fail ? "failed" : "ready",
            },
            points: [
              {
                id: "point",
                source_id: "source",
                provider_id: "arn:snapshot",
                name: "daily",
                kind: cloudSQL ? "FINAL" : "manual",
                status: cloudSQL ? "SUCCESSFUL" : "available",
                engine: "postgres",
                version: "17.2",
                encrypted: true,
                created_at: "2026-10-20T00:00:00Z",
                last_seen_at: "2026-10-21T00:00:00Z",
                present: true,
                expires_at:
                  expiresAt && !(dropExpiryOnRefresh && refreshes)
                    ? expiresAt
                    : null,
              },
            ],
          }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return () => refreshes
}
function show() {
  render(
    <ClientProvider client={forgeClient}>
      <NativeRecovery project="p" environment="prod" />
    </ClientProvider>
  )
}

it("shows native identity, incomplete observations and provider refresh without restore controls", async () => {
  const refreshes = fixture()
  show()
  expect(await screen.findByText("daily")).toBeInTheDocument()
  expect(screen.getByText(/db-original/)).toBeInTheDocument()
  expect(screen.getByText(/partial scan/i)).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: /test restore|delete|retire/i })
  ).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Refresh cloud snapshots" })
  )
  expect(refreshes()).toBe(1)
})

it("keeps saved points visible after a failed refresh and offers retry", async () => {
  fixture({ fail: true })
  show()
  await screen.findByText("daily")
  await userEvent.click(
    screen.getByRole("button", { name: "Refresh cloud snapshots" })
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    /could not refresh/i
  )
  expect(screen.getByText("daily")).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Refresh cloud snapshots" })
  ).toBeEnabled()
})

it("uses the shared empty state when no pinned history exists", async () => {
  fixture({ empty: true })
  show()
  expect(
    await screen.findByText("No captured cloud database history")
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Refresh cloud snapshots" })
  ).not.toBeInTheDocument()
})

it("requests the selected environment before source pagination", async () => {
  let selectedEnvironment = ""
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    selectedEnvironment = url.searchParams.get("environment_id") ?? ""
    return new Response(JSON.stringify({ sources: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  })
  show()
  await screen.findByText("No captured cloud database history")
  expect(selectedEnvironment).toBe("prod")
})

it("explains missing source proof in partial Cloud SQL inventory", async () => {
  fixture({ cloudSQL: true })
  show()
  await screen.findByText("daily")
  expect(
    screen.getByText(/matching source creation metadata/i)
  ).toBeInTheDocument()
  expect(screen.getByText(/point time/i)).toBeInTheDocument()
  expect(screen.getByText(/partial scan/i)).toBeInTheDocument()
})

it("keeps unknown expiry separate from permanent retention", async () => {
  fixture()
  show()
  await screen.findByText("daily")
  expect(screen.getByText(/expiry not reported/i)).toBeInTheDocument()
})

it.each(["2026-10-21T03:00:00Z", "2099-10-21T03:00:00Z"])(
  "shows provider expiry without erasing historical points: %s",
  async (expiresAt) => {
    fixture({ cloudSQL: true, expiresAt })
    show()
    await screen.findByText("daily")
    expect(screen.getByText(/provider expiry/i)).toBeInTheDocument()
    expect(screen.queryByText(/expiry not reported/i)).not.toBeInTheDocument()
    expect(screen.getByText(/^SUCCESSFUL · FINAL/)).toBeInTheDocument()
  }
)

it("clears a formerly reported expiry after refresh through the normalized cache", async () => {
  fixture({
    cloudSQL: true,
    expiresAt: "2026-10-21T03:00:00Z",
    dropExpiryOnRefresh: true,
  })
  show()
  expect(await screen.findByText(/provider expiry/i)).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Refresh cloud snapshots" })
  )
  expect(await screen.findByText(/expiry not reported/i)).toBeInTheDocument()
  expect(screen.queryByText(/provider expiry/i)).not.toBeInTheDocument()
})
