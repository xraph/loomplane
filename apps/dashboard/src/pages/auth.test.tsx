import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { AuthPage } from "@/pages/auth"

const router = vi.hoisted(() => ({ path: "/", navigate: vi.fn() }))

vi.mock("@/lib/router", () => ({
  usePath: () => router.path,
  useNavigate: () => router.navigate,
}))

vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))
vi.mock("@/components/brand-logo", () => ({ BrandLogo: () => null }))

// authsome's forms talk to the API; these stand in, signing in on a click.
vi.mock("@authsome/ui-components", () => ({
  SignInForm: ({ onSuccess }: { onSuccess: () => void }) => (
    <button onClick={onSuccess}>Sign in</button>
  ),
  SignUpForm: () => null,
}))

beforeEach(() => {
  router.navigate.mockReset()
})

describe("AuthPage", () => {
  it("leaves you on the page you asked for once you sign in", async () => {
    // GitHub's install redirect: its query carries a one-time code that
    // only this page can use.
    router.path = "/integrations/github/setup"
    render(<AuthPage mode="sign-in" />)
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(router.navigate).not.toHaveBeenCalled()
  })

  it("goes to the overview from the sign-in page itself", async () => {
    router.path = "/sign-in"
    render(<AuthPage mode="sign-in" />)
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(router.navigate).toHaveBeenCalledWith({ to: "/", replace: true })
  })
})
