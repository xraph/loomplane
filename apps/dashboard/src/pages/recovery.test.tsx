import { AuthProvider } from "@authsome/ui-react"
import { render as renderView, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { RecoveryPage } from "@/pages/recovery"

const context = vi.hoisted(() => ({ navigate: vi.fn() }))
const requests: { url: string; body: unknown }[] = []
let responseStatus = 200
const capture: typeof fetch = async (input, init) => {
  requests.push({
    url: String(input),
    body: JSON.parse(String(init?.body ?? "{}")),
  })
  return new Response(
    JSON.stringify(
      responseStatus < 400
        ? { status: "ok" }
        : { message: "Mail temporarily unavailable" }
    ),
    {
      status: responseStatus,
      headers: { "Content-Type": "application/json" },
    }
  )
}
const render = (children: ReactNode) =>
  renderView(
    <AuthProvider baseURL="https://loomplane.example/authsome" fetch={capture}>
      {children}
    </AuthProvider>
  )

vi.mock("@/lib/router", () => ({
  useNavigate: () => context.navigate,
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({ children }: { children: ReactNode }) => (
    <main>{children}</main>
  ),
}))
vi.mock("@/components/brand-logo", () => ({ BrandLogo: () => null }))

beforeEach(() => {
  requests.length = 0
  responseStatus = 200
  vi.clearAllMocks()
})

describe("account recovery", () => {
  it("shows a resend failure without claiming a code was sent", async () => {
    responseStatus = 503
    render(<RecoveryPage mode="verify-email" email="person@example.com" />)
    await userEvent.click(
      screen.getByRole("button", { name: "Send a new code" })
    )
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /unavailable|503|could not/i
    )
    expect(
      screen.queryByText(/a new code has been sent/)
    ).not.toBeInTheDocument()
  })

  it("requests a reset through the Authsome client", async () => {
    render(<RecoveryPage mode="forgot-password" />)
    await userEvent.type(
      screen.getByLabelText("Email address"),
      "person@example.com"
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Send reset link" })
    )
    expect(
      await screen.findByText(/Check your email for a reset link/)
    ).toBeInTheDocument()
    expect(requests).toEqual([
      {
        url: "https://loomplane.example/authsome/v1/forgot-password",
        body: { email: "person@example.com" },
      },
    ])
  })

  it("submits the decoded reset token and returns to sign-in", async () => {
    render(<RecoveryPage mode="reset-password" token="reset+secret" />)
    await userEvent.type(
      screen.getByLabelText("New password"),
      "New-correct-password-42"
    )
    await userEvent.type(
      screen.getByLabelText("Confirm password"),
      "New-correct-password-42"
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Reset password" })
    )
    await waitFor(() =>
      expect(context.navigate).toHaveBeenCalledWith({
        to: "/sign-in",
        replace: true,
      })
    )
    expect(requests).toEqual([
      {
        url: "https://loomplane.example/authsome/v1/reset-password",
        body: {
          token: "reset+secret",
          new_password: "New-correct-password-42",
        },
      },
    ])
  })

  it("offers a fresh reset request when the link has no token", () => {
    render(<RecoveryPage mode="reset-password" />)
    expect(
      screen.getByRole("link", { name: "Request a reset link" })
    ).toHaveAttribute("href", "/forgot-password")
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument()
  })

  it("binds an unauthenticated verification code to its email", async () => {
    render(<RecoveryPage mode="verify-email" email="person@example.com" />)
    await userEvent.type(screen.getByLabelText("Verification code"), "123456")
    await userEvent.click(screen.getByRole("button", { name: "Verify email" }))
    await waitFor(() => expect(requests).toHaveLength(1))
    expect(requests[0]).toEqual({
      url: "https://loomplane.example/authsome/v1/verify-email",
      body: { email: "person@example.com", code: "123456" },
    })
  })
})
