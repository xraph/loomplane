import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { setInstance } from "@/lib/instance"
import { AuthPage } from "@/pages/auth"

const router = vi.hoisted(() => ({ navigate: vi.fn() }))

const invite = vi.hoisted(() => ({ token: undefined as string | undefined }))

vi.mock("@/lib/invite", () => ({ usePendingInvite: () => invite.token }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => router.navigate,
}))

vi.mock("@/components/brand-logo", () => ({ BrandLogo: () => null }))

// Stand in for the API forms so their navigation callbacks can be checked.
vi.mock("@authsome/ui-components", () => ({
  SignInForm: ({ onSuccess }: { onSuccess?: () => void }) => (
    <button onClick={onSuccess}>Sign in</button>
  ),
  SignUpForm: ({ onSuccess }: { onSuccess: () => void }) => (
    <form aria-label="Create account">
      <input type="password" aria-label="Password" />
      <button type="button" onClick={onSuccess}>
        Sign up
      </button>
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
  it("lets the app restore the destination when authentication changes", async () => {
    render(<AuthPage mode="sign-in" />)
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(router.navigate).not.toHaveBeenCalled()
  })

  it("returns to sign-in after creating an account", async () => {
    render(<AuthPage mode="sign-up" />)
    await userEvent.click(screen.getByRole("button", { name: "Sign up" }))
    expect(router.navigate).toHaveBeenCalledWith({
      to: "/sign-in",
      replace: true,
    })
  })

  describe("sign-up with the waitlist on", () => {
    beforeEach(() => {
      setInstance({ setupPending: false, waitlist: true })
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
    render(<AuthPage mode="sign-up" />)
    expect(screen.getByLabelText("Password")).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Join the waitlist" })
    ).not.toBeInTheDocument()
  })
})
