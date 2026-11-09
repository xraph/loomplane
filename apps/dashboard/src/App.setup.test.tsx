import { render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import App from "@/App"
import { setInstance } from "@/lib/instance"

const auth = vi.hoisted(() => ({ isAuthenticated: false, isLoading: false }))
const route = vi.hoisted(() => ({ path: "/" }))

vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => undefined }))
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  usePath: () => route.path,
}))
// A stand-in for the router: it says where App sent you.
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Navigate: ({ to }: { to: string }) => <p>location {to}</p>,
}))
vi.mock("@/pages/setup", () => ({ SetupPage: () => <h1>Set up Loomplane</h1> }))
vi.mock("@/pages/auth", () => ({
  AuthPage: ({ mode }: { mode: string }) => <p>{mode} card</p>,
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

beforeEach(() => {
  auth.isAuthenticated = false
})

describe("first-run setup gating", () => {
  it.each(["/", "/sign-in"])(
    "sends a signed-out visitor at %s to setup",
    (path) => {
      setInstance({
        setupPending: true,
        waitlist: false,
        setupTokenRequired: false,
      })
      route.path = path
      render(<App />)
      expect(
        screen.getByRole("heading", { name: "Set up Loomplane" })
      ).toBeInTheDocument()
      expect(screen.getByText("location /setup")).toBeInTheDocument()
    }
  )

  it("shows setup at /setup without moving", () => {
    setInstance({
      setupPending: true,
      waitlist: false,
      setupTokenRequired: false,
    })
    route.path = "/setup"
    render(<App />)
    expect(
      screen.getByRole("heading", { name: "Set up Loomplane" })
    ).toBeInTheDocument()
    expect(screen.queryByText(/^location/)).not.toBeInTheDocument()
  })

  it("sends /setup to sign-in once setup is done", () => {
    setInstance({
      setupPending: false,
      waitlist: false,
      setupTokenRequired: false,
    })
    route.path = "/setup"
    render(<App />)
    expect(screen.getByText("location /sign-in")).toBeInTheDocument()
    expect(screen.queryByRole("heading")).not.toBeInTheDocument()
  })
})
