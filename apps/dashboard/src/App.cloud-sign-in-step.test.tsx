import { render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import App from "@/App"
import { setInstance } from "@/lib/instance"

const auth = vi.hoisted(() => ({
  isAuthenticated: true,
  isLoading: false,
  user: { email_verified: true, email: "a@example.com" },
}))
const route = vi.hoisted(() => ({ path: "/" }))
const invite = vi.hoisted(() => ({ token: undefined as string | undefined }))

vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => invite.token }))
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  usePath: () => route.path,
}))
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Navigate: ({ to }: { to: string }) => <p>location {to}</p>,
  Outlet: () => <p>outlet</p>,
  useRouter: () => ({ navigate: () => undefined }),
  useRouterState: ({
    select,
  }: {
    select?: (s: { location: object }) => unknown
  }) => {
    const location = { pathname: "/", search: "", href: "/", publicHref: "/" }
    return select ? select({ location }) : location
  },
}))
vi.mock("@/pages/cloud-sign-in-step", () => ({
  CloudSignInStep: () => <p>cloud sign-in step</p>,
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({
    title,
    children,
  }: {
    title: string
    children: React.ReactNode
  }) => <section aria-label={title}>{children}</section>,
}))
vi.mock("@/lib/organization", () => ({
  OrganizationGate: () => <p>signed-in shell</p>,
  useOrganization: () => ({}),
}))

const base = { setupPending: false, waitlist: false, setupTokenRequired: false }

beforeEach(() => {
  auth.isAuthenticated = true
  auth.user = { email_verified: true, email: "a@example.com" }
  invite.token = undefined
})

describe("cloud sign-in setup step gating", () => {
  it("sends a platform admin with the offer on to the step", () => {
    setInstance({ ...base, platformAdmin: true, cloudSignInOffer: true })
    route.path = "/"
    render(<App />)
    expect(screen.getByText("location /setup/cloud-sign-in")).toBeTruthy()
  })

  it("does not redirect once the offer is off", () => {
    setInstance({ ...base, platformAdmin: true, cloudSignInOffer: false })
    route.path = "/"
    render(<App />)
    expect(screen.queryByText(/^location/)).toBeNull()
    expect(screen.getByText("signed-in shell")).toBeTruthy()
  })

  it("shows the step at its own address, framed, without moving", () => {
    setInstance({ ...base, platformAdmin: true, cloudSignInOffer: true })
    route.path = "/setup/cloud-sign-in"
    render(<App />)
    expect(
      screen.getByRole("region", { name: "Set up cloud sign-in" })
    ).toBeTruthy()
    expect(screen.getByText("cloud sign-in step")).toBeTruthy()
    expect(screen.queryByText(/^location/)).toBeNull()
  })

  it("sends someone who isn't a platform admin home", () => {
    setInstance({ ...base, platformAdmin: false, cloudSignInOffer: false })
    route.path = "/setup/cloud-sign-in"
    render(<App />)
    expect(screen.getByText("location /")).toBeTruthy()
  })

  it.each(["/integrations/oauth/callback", "/integrations/github/setup"])(
    "leaves the full-load callback %s alone",
    (path) => {
      setInstance({ ...base, platformAdmin: true, cloudSignInOffer: true })
      route.path = path
      render(<App />)
      expect(screen.queryByText(/^location/)).toBeNull()
      expect(screen.getByText("signed-in shell")).toBeTruthy()
    }
  )

  it("verifies an unverified admin's email before the step", () => {
    setInstance({
      ...base,
      requireEmailVerification: true,
      platformAdmin: true,
      cloudSignInOffer: true,
    })
    auth.user = { email_verified: false, email: "a@example.com" }
    route.path = "/"
    render(<App />)
    expect(screen.getByText("location /verify-email")).toBeTruthy()
  })

  it("still reaches a pending invitation once the step is skipped", () => {
    setInstance({ ...base, platformAdmin: true, cloudSignInOffer: false })
    invite.token = "invite_1"
    route.path = "/"
    render(<App />)
    expect(screen.getByText("location /invitations/accept")).toBeTruthy()
    route.path = "/invitations/accept"
    render(<App />)
    expect(screen.getByText("outlet")).toBeTruthy()
  })
})
