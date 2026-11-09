import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { isUnprefixed } from "@/lib/workspace-url"
import { NewWorkspace, Onboarding } from "@/pages/onboarding"

const signOut = vi.hoisted(() => vi.fn(async () => {}))
const switchOrganization = vi.hoisted(() => vi.fn(async () => {}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: { email: "grace@acme.com" }, signOut }),
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
  await userEvent.click(
    screen.getByRole("button", { name: "Create workspace" })
  )
  expect(switchOrganization).toHaveBeenCalledWith("org-b")
})

it("leads back to the workspace you came from", () => {
  render(<NewWorkspace />)
  expect(screen.getByRole("link", { name: "Back to XRaph" })).toHaveAttribute(
    "href",
    "/"
  )
})

it("uses the onboarding frame, not the sign-in one, and says who's signed in", () => {
  render(<NewWorkspace />)
  expect(screen.getByText("grace@acme.com")).toBeInTheDocument()
  expect(screen.getByText("Loomplane by Xraph")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument()
})

it("offers to sign out when you have no workspace yet", async () => {
  const activate = vi.fn(async () => {})
  render(<Onboarding activate={activate} />)
  expect(
    screen.getByRole("heading", { name: "Create your workspace" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("link", { name: /Back to/ })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Sign out" }))
  expect(signOut).toHaveBeenCalled()
})

it("keeps the new-workspace address out of any workspace's slug", () => {
  expect(isUnprefixed("/workspaces/new")).toBe(true)
})
