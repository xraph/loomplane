import { StrictMode } from "react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { act, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { setWorkspaceSlug } from "@/lib/workspace-url"
import { createDashboardRouter } from "./router"

const auth = vi.hoisted(() => ({ isAuthenticated: false, isLoading: false }))
const listeners = vi.hoisted(() => new Set<() => void>())
vi.mock("@authsome/ui-react", async () => {
  const { useSyncExternalStore } = await import("react")
  return {
    useAuth: () => {
      useSyncExternalStore(
        (listener) => {
          listeners.add(listener)
          return () => {
            listeners.delete(listener)
          }
        },
        () => `${auth.isAuthenticated}:${auth.isLoading}`
      )
      return auth
    },
  }
})
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => undefined }))
vi.mock("@/lib/organization", () => ({
  OrganizationGate: () => <p>Protected page</p>,
  useOrganization: vi.fn(),
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))
vi.mock("@/pages/auth", () => ({
  AuthPage: ({ mode }: { mode: string }) => <p>{mode} card</p>,
}))

beforeEach(() => {
  auth.isAuthenticated = false
  auth.isLoading = false
  sessionStorage.clear()
  setWorkspaceSlug("")
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

async function open(path: string) {
  const history = createMemoryHistory({
    initialEntries: ["/sign-up", path],
    initialIndex: 1,
  })
  const router = createDashboardRouter(history)
  await router.load()
  const destination = history.location.href
  const view = render(
    <StrictMode>
      <RouterProvider router={router} />
    </StrictMode>
  )
  return { history, router, destination, ...view }
}

describe("authentication redirects", () => {
  it.each([
    "/",
    "/projects",
    "/acme/projects/p1?environment=env_1#activity",
    "/integrations/github/setup?code=one%2Btwo&installation_id=42",
    "/integrations/oauth/callback?state=state_1&code=one%2Btwo",
    "/invitations/accept?token=invite%2Bone",
    "/missing/page",
  ])(
    "redirects %s and restores the full address after sign-in",
    async (path) => {
      const { history, destination } = await open(path)
      await screen.findByText("sign-in card")
      expect(history.location.href).toBe("/sign-in")
      expect(history.length).toBe(2)
      await act(async () => {
        auth.isAuthenticated = true
        listeners.forEach((listener) => listener())
      })
      await waitFor(() => expect(history.location.href).toBe(destination))
    }
  )

  it("keeps the destination through sign-up and a reload", async () => {
    const first = await open("/acme/projects/p1?environment=env_1#activity")
    await screen.findByText("sign-in card")
    await act(async () => {
      await first.router.navigate({ to: "/sign-up" })
    })
    expect(await screen.findByText("sign-up card")).toBeInTheDocument()
    first.unmount()
    auth.isAuthenticated = true
    const second = await open("/sign-in")
    await waitFor(() =>
      expect(second.history.location.href).toBe(
        "/acme/projects/p1?environment=env_1#activity"
      )
    )
  })

  it.each([
    "https://example.com/",
    "//example.com/",
    "/\\\\example.com/",
    "/sign-in",
    "/sign-up",
  ])("rejects an unsafe or looping destination: %s", async (destination) => {
    sessionStorage.setItem("loomplane.auth.destination", destination)
    auth.isAuthenticated = true
    const { history } = await open("/sign-in")
    await waitFor(() => expect(history.location.href).toBe("/"))
    expect(sessionStorage.getItem("loomplane.auth.destination")).toBeNull()
  })

  it("clears a previous workspace before restoring another workspace", async () => {
    setWorkspaceSlug("previous")
    const { history } = await open("/acme/projects")
    await screen.findByText("sign-in card")
    await act(async () => {
      auth.isAuthenticated = true
      listeners.forEach((listener) => listener())
    })
    await waitFor(() => expect(history.location.href).toBe("/acme/projects"))
  })

  it("waits for authentication to load before redirecting", async () => {
    auth.isLoading = true
    const { history } = await open("/projects")
    expect(history.location.href).toBe("/projects")
    expect(screen.queryByText("sign-in card")).not.toBeInTheDocument()
  })

  it("returns a direct sign-in visit to the overview", async () => {
    auth.isAuthenticated = true
    const { history } = await open("/sign-in")
    await waitFor(() => expect(history.location.href).toBe("/"))
  })
})
