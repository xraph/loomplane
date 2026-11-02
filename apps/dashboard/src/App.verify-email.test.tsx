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
  setWorkspaceSlug("")
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => instance(false))

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

  it("changes nothing when verification isn't required", async () => {
    instance(false)
    await open("/projects")
    await screen.findByText("Protected page")
  })
})
