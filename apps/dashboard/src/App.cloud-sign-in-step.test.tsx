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

vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => undefined }))
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  usePath: () => route.path,
}))
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Navigate: ({ to }: { to: string }) => <p>location {to}</p>,
  Outlet: () => <p>outlet</p>,
  useRouter: () => ({ navigate: () => undefined }),
  useRouterState: () => ({ pathname: "/", search: "", href: "/" }),
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
})
