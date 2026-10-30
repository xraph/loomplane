import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { setInstance } from "@/lib/instance"
import { SetupPage } from "@/pages/setup"

const mocks = vi.hoisted(() => ({
  complete: vi.fn(),
  assign: vi.fn(),
}))

vi.mock("@/client/forge/loomplane/hooks", () => ({ useSetupComplete: {} }))
vi.mock("@/client/hooks", () => ({
  useMutation: () => ({ mutateAsync: mocks.complete, isPending: false }),
}))
// A client-side link is marked, so a test can tell it from a full load.
vi.mock("@/lib/router", () => ({
  Link: ({ to, children }: { to: string; children: React.ReactNode }) => (
    <a href={to} data-client-route="">
      {children}
    </a>
  ),
}))
vi.mock("@/components/brand-logo", () => ({ BrandLogo: () => null }))
vi.mock("@authsome/ui-components", () => ({
  AuthCard: ({
    title,
    description,
    children,
  }: {
    title: string
    description: string
    children: React.ReactNode
  }) => (
    <section>
      <h1>{title}</h1>
      <p>{description}</p>
      {children}
    </section>
  ),
}))

beforeEach(() => {
  mocks.complete.mockReset()
  mocks.assign.mockReset()
  visit("")
})

function visit(search: string, tokenRequired = false) {
  setInstance({
    setupPending: true,
    waitlist: false,
    setupTokenRequired: tokenRequired,
  })
  vi.stubGlobal("location", {
    ...window.location,
    search,
    assign: mocks.assign,
  })
}

async function fill(confirm = "correct horse battery") {
  await userEvent.type(screen.getByLabelText("Name"), "Ada Lovelace")
  await userEvent.type(screen.getByLabelText("Email"), "ada@example.com")
  await userEvent.type(
    screen.getByLabelText("Password"),
    "correct horse battery"
  )
  await userEvent.type(screen.getByLabelText("Confirm password"), confirm)
  await userEvent.type(screen.getByLabelText("Organization name"), "Analytical")
  await userEvent.click(screen.getByRole("button", { name: "Create account" }))
}

describe("SetupPage", () => {
  it("says what it creates", () => {
    render(<SetupPage />)
    expect(
      screen.getByText(
        "Create the first account. It owns this instance and its first organization."
      )
    ).toBeInTheDocument()
  })

  it("refuses mismatched passwords without a request", async () => {
    render(<SetupPage />)
    await fill("something else")
    const confirm = screen.getByLabelText("Confirm password")
    expect(confirm).toHaveAttribute("aria-invalid", "true")
    expect(confirm).toHaveAccessibleDescription("Passwords don't match.")
    expect(screen.getByLabelText("Organization name")).not.toHaveAttribute(
      "aria-invalid"
    )
    expect(mocks.complete).not.toHaveBeenCalled()
  })

  it("shows a server error without blaming the confirmation", async () => {
    mocks.complete.mockRejectedValue(
      Object.assign(
        new Error("the organization's name can be at most 64 characters"),
        { statusCode: 400 }
      )
    )
    render(<SetupPage />)
    await fill()
    expect(
      await screen.findByText(
        "The organization's name can be at most 64 characters."
      )
    ).toBeInTheDocument()
    expect(screen.getByLabelText("Confirm password")).not.toHaveAttribute(
      "aria-invalid"
    )
  })

  it("points to sign-in when setup is already done", async () => {
    mocks.complete.mockRejectedValue(
      Object.assign(new Error("setup has already been completed"), {
        statusCode: 403,
      })
    )
    render(<SetupPage />)
    await fill()
    expect(
      await screen.findByText("This instance is already set up.")
    ).toBeInTheDocument()
    // A full load, not a client route: the app still has setup as pending
    // and would send a client route straight back here.
    const signIn = screen.getByRole("link", { name: "Sign in" })
    expect(signIn).toHaveAttribute("href", "/sign-in")
    expect(signIn).not.toHaveAttribute("data-client-route")
  })

  it("reloads onto the dashboard on success", async () => {
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: false })
    render(<SetupPage />)
    await fill()
    expect(mocks.complete).toHaveBeenCalledWith({
      body: {
        name: "Ada Lovelace",
        email: "ada@example.com",
        password: "correct horse battery",
        organizationName: "Analytical",
      },
    })
    expect(mocks.assign).toHaveBeenCalledWith("/")
  })

  it("goes to verify-email when the address needs confirming", async () => {
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: true })
    render(<SetupPage />)
    await fill()
    expect(mocks.assign).toHaveBeenCalledWith(
      "/verify-email?email=ada%40example.com"
    )
  })

  it("sends the token from the link", async () => {
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: false })
    visit("?token=abc", true)
    render(<SetupPage />)
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument()
    await fill()
    expect(mocks.complete).toHaveBeenCalledWith({
      body: expect.objectContaining({ token: "abc" }),
    })
  })

  it("asks for the token when required and missing", async () => {
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: false })
    visit("", true)
    render(<SetupPage />)
    const field = screen.getByLabelText("Setup token")
    expect(field).toHaveAccessibleDescription(
      "Find it in the API's log, or run loomplane setup-token on the server."
    )
    await userEvent.type(field, "mfrggzdfmztwq2lknnwg23tpobyxe43u")
    await fill()
    expect(mocks.complete).toHaveBeenCalledWith({
      body: expect.objectContaining({
        token: "mfrggzdfmztwq2lknnwg23tpobyxe43u",
      }),
    })
  })

  it("has no token field in development", async () => {
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: false })
    visit("", false)
    render(<SetupPage />)
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument()
    await fill()
    const body = mocks.complete.mock.calls[0][0].body
    expect(body).not.toHaveProperty("token")
  })

  it("shows the server's message when the token is wrong", async () => {
    mocks.complete.mockRejectedValue(
      Object.assign(
        new Error(
          "a valid setup token is required; find it in the API's log or run loomplane setup-token"
        ),
        { statusCode: 403 }
      )
    )
    visit("", true)
    render(<SetupPage />)
    await userEvent.type(screen.getByLabelText("Setup token"), "wrong")
    await fill()
    expect(
      await screen.findByText(/A valid setup token is required/)
    ).toBeInTheDocument()
    expect(
      screen.queryByText("This instance is already set up.")
    ).not.toBeInTheDocument()
  })

  it("shows the token field after a stale link is refused", async () => {
    mocks.complete.mockRejectedValueOnce(
      Object.assign(
        new Error(
          "a valid setup token is required; find it in the API's log or run loomplane setup-token"
        ),
        { statusCode: 403 }
      )
    )
    mocks.complete.mockResolvedValue({ ok: true, verificationRequired: false })
    visit("?token=stale", true)
    render(<SetupPage />)
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument()
    await fill()
    expect(
      await screen.findByText(/A valid setup token is required/)
    ).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText("Setup token"), "fresh")
    await userEvent.click(screen.getByRole("button", { name: "Create account" }))
    await waitFor(() =>
      expect(mocks.complete).toHaveBeenLastCalledWith({
        body: expect.objectContaining({ token: "fresh" }),
      })
    )
  })
})
