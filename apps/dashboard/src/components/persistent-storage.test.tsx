import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { PersistentStorage } from "@/components/persistent-storage"

vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <PersistentStorage environmentNames={new Map([["env", "production"]])} />
    </ClientProvider>
  )
}
function fixture({ denied = false, empty = false } = {}) {
  let fail = denied
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    let body: unknown = {}
    let status = 200
    if (url.endsWith("/connections"))
      body = {
        connections: [
          {
            id: "connection",
            name: "Database host",
            cloud: "server",
            status: "ready",
          },
        ],
      }
    if (url.endsWith("/projects"))
      body = { projects: [{ id: "project", name: "Application database" }] }
    if (url.endsWith("/storage")) {
      status = fail ? 403 : 200
      body = fail
        ? { error: "Storage access denied" }
        : {
            volumes: empty
              ? []
              : [
                  {
                    id: "volume",
                    name: "lpv-volume",
                    project_id: "project",
                    environment_id: "env",
                    instance_id: "instance",
                    connection_id: "connection",
                    role: "data",
                    provider: "server",
                    status: "retained",
                    capacity_bytes: 0,
                  },
                ],
          }
    }
    return new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })
  })
  return () => {
    fail = false
  }
}

it("shows retained ownership without inventing a disk quota or a delete action", async () => {
  fixture()
  mount()
  expect(await screen.findByText("lpv-volume")).toBeInTheDocument()
  expect(screen.getByText("Application database")).toBeInTheDocument()
  expect(screen.getByText("Retained")).toBeInTheDocument()
  expect(screen.getAllByText("production").length).toBeGreaterThan(0)
  expect(screen.getAllByText("Shared host capacity").length).toBeGreaterThan(0)
  expect(
    screen.queryByRole("button", { name: /delete|destroy/i })
  ).not.toBeInTheDocument()
})

it("keeps denied storage distinct from empty and retries the failed read", async () => {
  const allow = fixture({ denied: true })
  mount()
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Storage access denied"
  )
  expect(
    screen.queryByText("No persistent volumes found")
  ).not.toBeInTheDocument()
  allow()
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("lpv-volume")).toBeInTheDocument()
})

it("explains an empty inventory through the shared zero state", async () => {
  fixture({ empty: true })
  mount()
  expect(
    await screen.findByText("No persistent volumes found")
  ).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Deploy a service" })
  ).toHaveAttribute("href", "/services")
})
