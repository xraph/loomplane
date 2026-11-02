import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import App from "@/App"

const auth = vi.hoisted(() => ({ isAuthenticated: false, isLoading: false }))
const route = vi.hoisted(() => ({ path: "/sign-in" }))
const frame = vi.hoisted(() => ({ mounts: 0 }))

vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/invite", () => ({ usePendingInvite: () => undefined }))
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  usePath: () => route.path,
}))
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Outlet: () => <p>Recovery card</p>,
}))
vi.mock("@/pages/auth", () => ({
  AuthPage: ({ mode }: { mode: string }) => <p>{mode} card</p>,
}))
vi.mock("@/components/auth-layout", async () => {
  const { useEffect } = await import("react")
  return {
    AuthLayout: ({
      title,
      children,
    }: {
      title: string
      children: React.ReactNode
    }) => {
      useEffect(() => {
        frame.mounts += 1
      }, [])
      return (
        <section aria-label={title} data-testid="frame">
          {children}
        </section>
      )
    },
  }
})

// One frame for every signed-out screen: moving between them must swap the
// card without remounting the layout, or its light and animations restart.
describe("shared auth layout", () => {
  it("keeps one frame from sign-in through sign-up and recovery", () => {
    frame.mounts = 0
    route.path = "/sign-in"
    const { rerender } = render(<App />)
    expect(screen.getByText("sign-in card")).toBeInTheDocument()

    for (const [path, card, title] of [
      ["/sign-up", "sign-up card", "Create your Loomplane account"],
      ["/forgot-password", "Recovery card", "Recover your Loomplane account"],
      ["/verify-email", "Recovery card", "Verify your email"],
      ["/sign-in", "sign-in card", "Sign in to Loomplane"],
    ]) {
      route.path = path
      rerender(<App />)
      expect(screen.getByText(card)).toBeInTheDocument()
      expect(screen.getByTestId("frame")).toHaveAccessibleName(title)
    }
    expect(frame.mounts).toBe(1)
  })
})
