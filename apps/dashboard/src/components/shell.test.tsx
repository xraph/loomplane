import { ClientProvider } from "@forge-go/client-react"
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { AppSidebar } from "@/components/app-sidebar"
import { ShellProvider, useShell } from "@/components/shell"
import { ThemeProvider } from "@/components/theme-provider"
import { SidebarProvider } from "@/components/ui/sidebar"
import { AgentProvider, FeaturesProvider } from "@/lib/features"

const { navigate, access } = vi.hoisted(() => ({
  navigate: vi.fn(),
  access: { denied: [] as string[] },
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  usePath: () => "/",
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: (permission: string) => !access.denied.includes(permission),
  }),
}))
// What the sidebar reads besides the router and the organization.
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string; search: object } }) => unknown
  }) => select({ location: { pathname: "/", search: {} } }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  navigate.mockReset()
  forgeClient.clear()
  access.denied = []
})

function stubEmpty() {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ projects: [], installations: [] }), {
        headers: { "Content-Type": "application/json" },
      })
  )
}

function Wrapped({ children }: { children: ReactNode }) {
  return (
    <ClientProvider client={forgeClient}>
      <ThemeProvider>
        <ShellProvider>{children}</ShellProvider>
      </ThemeProvider>
    </ClientProvider>
  )
}

function Entry() {
  const { openDeploy } = useShell()
  return (
    <button
      onClick={() => openDeploy({ project: "p", environment: "preview" })}
    >
      Deploy this project
    </button>
  )
}
it("opens the full deployment configuration with the project and environment preset", async () => {
  stubEmpty()
  render(
    <Wrapped>
      <Entry />
    </Wrapped>
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Deploy this project" })
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/new",
    search: { project: "p", environment: "preview" },
  })
})

function sidebar(ai: boolean) {
  stubEmpty()
  render(
    <Wrapped>
      <FeaturesProvider features={{ ai }}>
        <SidebarProvider>
          <AppSidebar />
        </SidebarProvider>
      </FeaturesProvider>
    </Wrapped>
  )
}

it("shows no AI group when AI is off", () => {
  sidebar(false)
  expect(screen.queryByText("AI")).toBeNull()
  expect(document.querySelector('a[href^="/ai/"]')).toBeNull()
})

it("shows no AI group without ai:read", () => {
  access.denied = ["ai:read", "ai:manage"]
  sidebar(true)
  expect(screen.queryByText("AI")).toBeNull()
  expect(document.querySelector('a[href^="/ai/"]')).toBeNull()
})

it("keeps AI settings in the workspace's Settings for members", async () => {
  // A developer reads AI and manages none of it.
  access.denied = ["ai:manage"]
  sidebar(true)
  // One Settings in the sidebar, so the AI group can't be mistaken for it.
  expect(screen.getAllByText("Settings")).toHaveLength(1)
  await userEvent.click(screen.getByText("Settings"))
  expect(screen.getByRole("link", { name: "AI" })).toHaveAttribute(
    "href",
    "/ai/settings"
  )
})

it("leaves AI out of Settings when AI is off", async () => {
  sidebar(false)
  await userEvent.click(screen.getByText("Settings"))
  expect(screen.getByRole("link", { name: "API keys" })).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "AI" })).toBeNull()
})

function aiLinks() {
  const group = screen
    .getByText("AI")
    .closest('[data-sidebar="group"]') as HTMLElement
  return Array.from(group.querySelectorAll("a")).map((a) => [
    a.textContent?.trim(),
    a.getAttribute("href"),
  ])
}

it("adds Models to the AI group", () => {
  access.denied = ["ai:manage"]
  sidebar(true)
  expect(aiLinks()).toEqual([["Models", "/ai/models"]])
})

const asked: string[] = []

function serveAgent(enabled: boolean) {
  asked.length = 0
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    asked.push(path)
    if (path === "/api/v1/agent/status")
      return Response.json({ server: true, enabled, knowledge: false })
    return Response.json({
      projects: [],
      installations: [],
      requests: [],
      conversations: [],
      members: [],
    })
  })
}

function withAgent(ai: boolean, enabled = true) {
  serveAgent(enabled)
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai }}>
        <AgentProvider>
          <ThemeProvider>
            <ShellProvider>
              <p>Page</p>
            </ShellProvider>
          </ThemeProvider>
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
}

// pressJ sends ⌘J and reports whether the shell took it.
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

it("cmd+J opens the assistant when the agent is on", async () => {
  withAgent(true)
  await waitFor(() => {
    act(() => {
      expect(pressJ()).toBe(true)
    })
  })
  expect(
    await screen.findByRole("heading", { name: "Assistant" })
  ).toBeVisible()
  // ⌘J again puts it away.
  act(() => {
    pressJ()
  })
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Assistant" })).toBeNull()
  )
})

it("cmd+J does nothing when the agent is off", async () => {
  withAgent(false)
  await screen.findByText("Page")
  let prevented = true
  act(() => {
    prevented = pressJ()
  })
  expect(prevented).toBe(false)
  expect(screen.queryByRole("heading", { name: "Assistant" })).toBeNull()

  // Nor with AI on and the workspace's agent off.
  cleanup()
  withAgent(true, false)
  await waitFor(() => expect(asked).toContain("/api/v1/agent/status"))
  act(() => {
    prevented = pressJ()
  })
  expect(prevented).toBe(false)
  expect(screen.queryByRole("heading", { name: "Assistant" })).toBeNull()
})

it("the command menu offers Ask the agent only when on", async () => {
  withAgent(true)
  // Once the agent's status is in, the shortcut sheet lists ⌘J too.
  await waitFor(() => {
    act(() => {
      expect(pressJ()).toBe(true)
    })
  })
  act(() => {
    pressJ()
  })
  await userEvent.keyboard("{Meta>}k{/Meta}")
  const item = await screen.findByRole("option", { name: /Ask the agent/ })
  expect(item).toHaveTextContent("⌘J")
  await userEvent.click(item)
  expect(
    await screen.findByRole("heading", { name: "Assistant" })
  ).toBeVisible()

  cleanup()
  withAgent(false)
  await screen.findByText("Page")
  await userEvent.keyboard("{Meta>}k{/Meta}")
  await screen.findByRole("option", { name: /Toggle theme/ })
  expect(screen.queryByRole("option", { name: /Ask the agent/ })).toBeNull()
})

it("lists cmd+J among the shortcuts only when the agent is on", async () => {
  withAgent(true)
  await waitFor(() => {
    act(() => {
      expect(pressJ()).toBe(true)
    })
  })
  act(() => {
    pressJ()
  })
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Assistant" })).toBeNull()
  )
  await userEvent.keyboard("?")
  const sheet = await screen.findByRole("dialog")
  expect(within(sheet).getByText("Ask the agent")).toBeInTheDocument()

  cleanup()
  withAgent(false)
  await screen.findByText("Page")
  await userEvent.keyboard("?")
  const off = await screen.findByRole("dialog")
  expect(within(off).getByText("Open the command menu")).toBeInTheDocument()
  expect(within(off).queryByText("Ask the agent")).toBeNull()
})
