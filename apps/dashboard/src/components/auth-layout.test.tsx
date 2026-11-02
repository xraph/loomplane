import { fireEvent, render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { AuthLayout } from "@/components/auth-layout"

const navigate = vi.hoisted(() => vi.fn())

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  useNavigate: () => navigate,
}))
// jsdom has no WebGL
vi.mock("@/components/auth-silk", () => ({ AuthSilk: () => null }))
vi.mock("@/components/mode-toggle", () => ({ ModeToggle: () => null }))

function renderWithLinks() {
  render(
    <AuthLayout title="Sign in to Loomplane">
      <a href="/sign-up">Sign up</a>
      <a href="/forgot-password?email=a%40b.co">Forgot password?</a>
      <a href="/projects">Projects</a>
    </AuthLayout>
  )
}

// Authsome links between the auth screens with plain anchors. The frame turns
// those into client-side navigation so it isn't torn down and rebuilt.
describe("AuthLayout links", () => {
  beforeEach(() => navigate.mockClear())

  it("routes links between auth screens without reloading", () => {
    renderWithLinks()
    const click = fireEvent.click(screen.getByText("Sign up"))
    expect(click).toBe(false) // default prevented
    expect(navigate).toHaveBeenCalledWith({ href: "/sign-up" })

    fireEvent.click(screen.getByText("Forgot password?"))
    expect(navigate).toHaveBeenLastCalledWith({
      href: "/forgot-password?email=a%40b.co",
    })
  })

  it("leaves other links, new tabs and the Xraph link alone", () => {
    renderWithLinks()
    expect(fireEvent.click(screen.getByText("Projects"))).toBe(true)
    expect(
      fireEvent.click(screen.getByText("Sign up"), { metaKey: true })
    ).toBe(true)
    expect(fireEvent.click(screen.getByText("Xraph"))).toBe(true)
    expect(navigate).not.toHaveBeenCalled()
  })
})
