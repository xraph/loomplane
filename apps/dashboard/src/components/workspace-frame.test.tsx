import { ClientProvider } from "@forge-go/client-react"
import { act, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ThemeProvider } from "@/components/theme-provider"
import { AgentDock } from "@/components/assistant/agent-dock"
import { WorkspaceFrame } from "@/components/workspace-frame"
import { progressKey } from "@/learn/progress"
import { FeaturesProvider } from "@/lib/features"

vi.mock("@/lib/router", () => ({
  useNavigate: () => () => {},
  usePath: () => "/",
  useSearchParams: () => ({}),
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

// The frame App puts every workspace page in, with only the agent's status
// to read: the shell's keys, menu and panel must see the agent as on.
function show() {
  // A workspace with nothing in it greets you first; these tests are about
  // the agent, so the welcome counts as seen.
  localStorage.setItem(
    progressKey("usr_1", "org"),
    JSON.stringify({ welcomed: true })
  )
  const asked: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    asked.push(path)
    if (path === "/api/v1/agent/status")
      return Response.json({ server: true, enabled: true, knowledge: false })
    return Response.json({
      projects: [],
      requests: [],
      conversations: [],
      members: [],
      installations: [],
    })
  })
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai: true }}>
        <ThemeProvider>
          <WorkspaceFrame orgId="org">
            <p>Page</p>
            <AgentDock />
          </WorkspaceFrame>
        </ThemeProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
  return asked
}

function pressJ(): boolean {
  const event = new KeyboardEvent("keydown", {
    key: "j",
    metaKey: true,
    bubbles: true,
    cancelable: true,
  })
  window.dispatchEvent(event)
  return event.defaultPrevented
}

it("lets cmd+J open the agent's panel in a workspace with the agent on", async () => {
  const asked = show()
  await waitFor(() => expect(asked).toContain("/api/v1/agent/status"))
  await waitFor(() => {
    act(() => {
      expect(pressJ()).toBe(true)
    })
  })
  expect(
    await screen.findByRole("complementary", { name: "Agent" })
  ).toBeVisible()
})

it("lists the agent in the command menu in a workspace with the agent on", async () => {
  const asked = show()
  await waitFor(() => expect(asked).toContain("/api/v1/agent/status"))
  // Once the agent's status is in, the shell takes cmd+J; put the panel
  // away again and open the menu.
  await waitFor(() => {
    act(() => {
      expect(pressJ()).toBe(true)
    })
  })
  act(() => {
    pressJ()
  })
  await waitFor(() =>
    expect(screen.queryByRole("complementary", { name: "Agent" })).toBeNull()
  )
  await userEvent.keyboard("{Meta>}k{/Meta}")
  expect(
    await screen.findByRole("option", { name: /Ask the agent/ })
  ).toHaveTextContent("⌘J")
})
