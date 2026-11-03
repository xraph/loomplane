import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { setInstance } from "@/lib/instance"
import { setWorkspaceSlug } from "@/lib/workspace-url"
import { createDashboardRouter } from "./router"

const auth = vi.hoisted(() => ({
  isAuthenticated: true,
  isLoading: false,
  user: { email: "ada@example.com", email_verified: false } as {
    email: string
    email_verified: boolean
  } | null,
}))
vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => undefined }))
vi.mock("@/lib/organization", () => ({
  OrganizationGate: () => <p>Protected page</p>,
  useOrganization: vi.fn(),
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))
vi.mock("@/pages/recovery", () => ({
  RecoveryPage: ({ mode, email }: { mode: string; email?: string }) => (
    <p>
      {mode} card for {email}
    </p>
  ),
}))

const assign = vi.hoisted(() => vi.fn())

function instance(requireEmailVerification: boolean) {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    requireEmailVerification,
  })
}

beforeEach(() => {
  auth.isAuthenticated = true
  auth.user = { email: "ada@example.com", email_verified: false }
  sessionStorage.clear()
  assign.mockReset()
  vi.stubGlobal("location", { ...window.location, assign })
  setWorkspaceSlug("")
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => {
  instance(false)
  vi.unstubAllGlobals()
})

async function open(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] })
  const router = createDashboardRouter(history)
  await router.load()
  render(<RouterProvider router={router} />)
  return history
}

describe("email verification", () => {
  // Sign-up leaves a session cookie behind, so a second tab arrives here
  // signed in with an address nobody has verified.
  it("sends a signed-in, unverified account to verify its email", async () => {
    instance(true)
    const history = await open("/projects")
    await screen.findByText("verify-email card for ada@example.com")
    await waitFor(() => expect(history.location.pathname).toBe("/verify-email"))
    expect(screen.queryByText("Protected page")).toBeNull()
  })

  it("lets a verified account in", async () => {
    instance(true)
    auth.user = { email: "ada@example.com", email_verified: true }
    await open("/projects")
    await screen.findByText("Protected page")
  })

  // Nothing is left to verify, so the page would only ask for a code that
  // no longer exists.
  it("sends a verified account home from the verify page", async () => {
    instance(true)
    auth.user = { email: "ada@example.com", email_verified: true }
    await open("/verify-email?email=ada%40example.com")
    // A full load: the public config was read before the account verified,
    // so only a fresh read can say whether to offer cloud sign-in.
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"))
  })

  // Signed out, the page is how a new account gets its first session.
  it("keeps the verify page for someone signed out", async () => {
    instance(true)
    auth.isAuthenticated = false
    auth.user = null
    await open("/verify-email?email=ada%40example.com")
    await screen.findByText("verify-email card for ada@example.com")
  })

  it("changes nothing when verification isn't required", async () => {
    instance(false)
    await open("/projects")
    await screen.findByText("Protected page")
  })
})
