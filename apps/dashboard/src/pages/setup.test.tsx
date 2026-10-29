import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { SetupPage } from "@/pages/setup"

const mocks = vi.hoisted(() => ({
  complete: vi.fn(),
  assign: vi.fn(),
}))

vi.mock("@/client/forge/loomplane/hooks", () => ({ useSetupComplete: {} }))
vi.mock("@/client/hooks", () => ({
  useMutation: () => ({ mutateAsync: mocks.complete, isPending: false }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ to, children }: { to: string; children: React.ReactNode }) => (
    <a href={to}>{children}</a>
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
  vi.stubGlobal("location", { ...window.location, assign: mocks.assign })
})

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
    expect(screen.getByText("Passwords don't match.")).toBeInTheDocument()
    expect(mocks.complete).not.toHaveBeenCalled()
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
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/sign-in"
    )
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
})
