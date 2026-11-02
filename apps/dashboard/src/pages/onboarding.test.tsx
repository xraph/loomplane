import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { isUnprefixed } from "@/lib/workspace-url"
import { NewWorkspace, Onboarding } from "@/pages/onboarding"

const signOut = vi.hoisted(() => vi.fn(async () => {}))
const switchOrganization = vi.hoisted(() => vi.fn(async () => {}))
vi.mock("@authsome/ui-react", () => ({ useAuth: () => ({ signOut }) }))
vi.mock("@authsome/ui-components", () => ({
  AuthCard: ({
    title,
    children,
    footer,
  }: {
    title: string
    children: React.ReactNode
    footer: React.ReactNode
  }) => (
    <section>
      <h2>{title}</h2>
      {children}
      {footer}
    </section>
  ),
}))
vi.mock("@/components/auth-layout", () => ({
  AuthLayout: ({
    title,
    children,
  }: {
    title: string
    children: React.ReactNode
  }) => (
    <main>
      <h1>{title}</h1>
      {children}
    </main>
  ),
}))
vi.mock("@/components/organization-form", () => ({
  OrganizationForm: ({
    onCreated,
  }: {
    onCreated: (orgId: string) => Promise<void>
  }) => (
    <button type="button" onClick={() => void onCreated("org-b")}>
      Create workspace
    </button>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org-a" },
    organizations: [{ id: "org-a", name: "XRaph", slug: "xraph" }],
    switchOrganization,
  }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ to, ...props }: { to: string }) => <a href={to} {...props} />,
}))

afterEach(() => {
  cleanup()
  signOut.mockClear()
  switchOrganization.mockClear()
})

it("starts a second workspace in the onboarding frame and switches to it", async () => {
  render(<NewWorkspace />)
  expect(
    screen.getByRole("heading", { name: "Create another workspace" })
  ).toBeInTheDocument()
  expect(
    screen.getByRole("heading", { name: "Name your workspace" })
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Create workspace" })
  )
  expect(switchOrganization).toHaveBeenCalledWith("org-b")
})

it("leads back to the workspace you came from instead of signing out", () => {
  render(<NewWorkspace />)
  expect(screen.getByRole("link", { name: "Back to XRaph" })).toHaveAttribute(
    "href",
    "/"
  )
  expect(
    screen.queryByRole("button", { name: "Sign out" })
  ).not.toBeInTheDocument()
})

it("offers to sign out when you have no workspace yet", async () => {
  const activate = vi.fn(async () => {})
  render(<Onboarding activate={activate} />)
  expect(
    screen.getByRole("heading", { name: "Create your workspace" })
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Sign out" }))
  expect(signOut).toHaveBeenCalled()
})

it("keeps the new-workspace address out of any workspace's slug", () => {
  expect(isUnprefixed("/workspaces/new")).toBe(true)
})
