import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { NewFromRepositoryPage } from "@/pages/new-from-repository"

let allowed = true
const navigate = vi.fn()
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => allowed }),
}))
vi.mock("@/lib/router", () => ({ useNavigate: () => navigate }))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/pages/projects", () => ({ Projects: () => <p>Projects list</p> }))
vi.mock("@/components/new-project-dialog", () => ({
  NewProjectDialog: ({
    repository,
    onOpenChange,
  }: {
    repository?: string
    onOpenChange: (open: boolean) => void
  }) => (
    <div role="dialog" aria-label="New project">
      <span>{repository ?? "no repository"}</span>
      <button type="button" onClick={() => onOpenChange(false)}>
        Cancel
      </button>
    </div>
  ),
}))

afterEach(() => {
  cleanup()
  navigate.mockReset()
  allowed = true
})

test.each([
  ["https://github.com/acme/shop", "acme/shop"],
  ["https://github.com/acme/shop/tree/main", "acme/shop"],
  ["acme/shop", "acme/shop"],
  ["https://gitlab.com/acme/tools/shop.git", "gitlab.com/acme/tools/shop"],
])("a button's repo %s opens New project with %s", (repo, shown) => {
  render(<NewFromRepositoryPage repo={repo} />)
  expect(screen.getByText("Projects list")).toBeInTheDocument()
  expect(screen.getByRole("dialog", { name: "New project" })).toHaveTextContent(
    shown
  )
})

test("a repo that isn't a repository still opens an empty New project", () => {
  render(<NewFromRepositoryPage repo="not a repo" />)
  expect(screen.getByRole("dialog")).toHaveTextContent("no repository")
})

test("closing the dialog leaves the projects list in its place", async () => {
  render(<NewFromRepositoryPage repo="acme/shop" />)
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(navigate).toHaveBeenCalledWith({ to: "/projects", replace: true })
})

test("without project access it says who can create one", () => {
  allowed = false
  render(<NewFromRepositoryPage repo="https://github.com/acme/shop" />)
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(screen.getByText("Project access required")).toBeInTheDocument()
  expect(screen.getByText("acme/shop")).toBeInTheDocument()
})
