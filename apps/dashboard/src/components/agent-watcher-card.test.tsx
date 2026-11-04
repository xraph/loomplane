import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AgentWatcherCard } from "./agent-watcher-card"

vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function serve(watcher: object) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path === "/api/v1/agent/watcher") return Response.json(watcher)
    return Response.json({ message: `unexpected ${path}` }, { status: 404 })
  })
}

function show() {
  render(
    <ClientProvider client={forgeClient}>
      <AgentWatcherCard />
    </ClientProvider>
  )
}

it("names the installations that haven't accepted issues", async () => {
  serve({
    paused: false,
    ticked_at: new Date(Date.now() - 2 * 60_000).toISOString(),
    issues: {
      configured: true,
      installations: [
        {
          account: "acme",
          account_type: "Organization",
          accepted: true,
          manage_url:
            "https://github.com/organizations/acme/settings/installations/1",
        },
        {
          account: "ada",
          account_type: "User",
          accepted: false,
          manage_url: "https://github.com/settings/installations/2",
        },
      ],
    },
  })
  show()
  expect(await screen.findByText("Watching")).toBeInTheDocument()
  expect(screen.getByText(/Last check 2 minutes ago/)).toBeInTheDocument()
  expect(screen.getByText(/Accepted/)).toHaveTextContent("acme")
  expect(
    screen.getByText(
      "ada hasn't accepted the Issues permission yet, so reports for its repositories go to Herald only."
    )
  ).toBeInTheDocument()
  const links = screen.getAllByRole("link", { name: "Review on GitHub" })
  expect(links).toHaveLength(1)
  expect(links[0]).toHaveAttribute(
    "href",
    "https://github.com/settings/installations/2"
  )
})

it("says Herald only when no GitHub App is configured", async () => {
  serve({ paused: false, issues: { configured: false, installations: [] } })
  show()
  expect(
    await screen.findByText(
      "No GitHub App on this server, so reports go to Herald only."
    )
  ).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Review on GitHub" })).toBeNull()
})

it("says when the watcher stopped investigating for the budget", async () => {
  serve({
    paused: true,
    paused_reason: "budget",
    paused_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    issues: { configured: false, installations: [] },
  })
  show()
  expect(
    await screen.findByText("Stopped investigating (budget)")
  ).toBeInTheDocument()
  expect(screen.getByText(/3 hours ago/)).toBeInTheDocument()
})
