import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { setInstance } from "@/lib/instance"
import { AuthPage } from "@/pages/auth"

const router = vi.hoisted(() => ({ path: "/", navigate: vi.fn() }))

const invite = vi.hoisted(() => ({ token: undefined as string | undefined }))

vi.mock("@/lib/invite", () => ({ usePendingInvite: () => invite.token }))
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
  SignUpForm: () => (
    <form aria-label="Create account">
      <input type="password" aria-label="Password" />
    </form>
  ),
  WaitlistForm: () => <button>Join the waitlist</button>,
}))

beforeEach(() => {
  router.navigate.mockReset()
  invite.token = undefined
  setInstance({ setupPending: false, waitlist: false })
  window.history.replaceState(null, "", "/")
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

  describe("sign-up with the waitlist on", () => {
    beforeEach(() => {
      setInstance({ setupPending: false, waitlist: true })
      router.path = "/sign-up"
    })

    it("asks to join the waitlist instead of taking a password", () => {
      render(<AuthPage mode="sign-up" />)
      expect(
        screen.getByRole("button", { name: "Join the waitlist" })
      ).toBeInTheDocument()
      expect(screen.queryByLabelText("Password")).not.toBeInTheDocument()
    })

    it("shows the sign-up form to someone with an invitation", () => {
      invite.token = "tok"
      render(<AuthPage mode="sign-up" />)
      expect(screen.getByLabelText("Password")).toBeInTheDocument()
    })

    it("shows the sign-up form when the address names an email", () => {
      window.history.replaceState(null, "", "/sign-up?email=ada%40example.com")
      render(<AuthPage mode="sign-up" />)
      expect(screen.getByLabelText("Password")).toBeInTheDocument()
    })

    it("switches to the sign-up form for an approved entry", async () => {
      render(<AuthPage mode="sign-up" />)
      await userEvent.click(
        screen.getByRole("button", {
          name: "Approved already? Create your account",
        })
      )
      expect(screen.getByLabelText("Password")).toBeInTheDocument()
    })
  })

  it("shows the sign-up form when the waitlist is off", () => {
    router.path = "/sign-up"
    render(<AuthPage mode="sign-up" />)
    expect(screen.getByLabelText("Password")).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Join the waitlist" })
    ).not.toBeInTheDocument()
  })
})
