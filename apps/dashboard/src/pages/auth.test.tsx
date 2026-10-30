import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { AuthPage } from "@/pages/auth"

const router = vi.hoisted(() => ({ navigate: vi.fn() }))

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
    <button onClick={onSuccess}>Sign up</button>
  ),
}))

beforeEach(() => {
  router.navigate.mockReset()
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
})
