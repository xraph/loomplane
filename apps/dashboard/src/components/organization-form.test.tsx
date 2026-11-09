import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { OrganizationForm } from "@/components/organization-form"

const taken = vi.hoisted(() => new Set(["acme"]))
const checks = vi.hoisted(() => [] as string[])
const create = vi.hoisted(() =>
  vi.fn<
    (input: { body: { name: string; slug: string } }) => Promise<{ id: string }>
  >(async () => ({ id: "org-new" }))
)
vi.mock("@/client/forge/authsome/hooks", () => ({
  useCheckOrgSlug: ({ query }: { query: { slug: string } }) => ({
    refetch: async () => {
      checks.push(query.slug)
      return { available: !taken.has(query.slug) }
    },
  }),
  useCreateOrganization: () => undefined,
}))
vi.mock("@/client/hooks", () => ({
  useMutation: () => ({ mutateAsync: create }),
}))
vi.mock("@/client/runtime", () => ({ authsomeApp: () => "app" }))

afterEach(() => {
  cleanup()
  checks.length = 0
  create.mockClear()
})

const address = () => screen.getByRole("textbox", { name: "Address" })

it("suggests an address from the name and says when it's free", async () => {
  render(<OrganizationForm onCreated={async () => {}} />)
  await userEvent.type(
    screen.getByRole("textbox", { name: /Workspace name/ }),
    "Acme Inc"
  )
  expect(address()).toHaveValue("acme-inc")
  expect(
    await screen.findByText(
      "acme-inc is free. It's yours once you create the workspace."
    )
  ).toBeInTheDocument()
  expect(screen.getByText("Free")).toBeInTheDocument()
  expect(screen.getAllByText("acme-inc").length).toBeGreaterThan(0)
})

it("refuses a taken or reserved address before you can create it", async () => {
  render(<OrganizationForm onCreated={async () => {}} />)
  await userEvent.type(
    screen.getByRole("textbox", { name: /Workspace name/ }),
    "Acme"
  )
  expect(await screen.findByText("Taken")).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Create workspace" })
  ).toBeDisabled()
  await userEvent.clear(address())
  await userEvent.type(address(), "settings")
  expect(screen.getByText("Reserved")).toBeInTheDocument()
  expect(checks).not.toContain("settings")
})

it("keeps an address you edited when the name changes, and creates with it", async () => {
  const onCreated = vi.fn(async () => {})
  render(<OrganizationForm onCreated={onCreated} />)
  const name = screen.getByRole("textbox", { name: /Workspace name/ })
  await userEvent.type(name, "Acme Inc")
  await userEvent.clear(address())
  await userEvent.type(address(), "acme-eng")
  await userEvent.type(name, " Engineering")
  expect(address()).toHaveValue("acme-eng")
  await screen.findByText("Free")
  await act(async () =>
    userEvent.click(screen.getByRole("button", { name: "Create workspace" }))
  )
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("org-new"))
  expect(create).toHaveBeenCalledWith({
    body: { name: "Acme Inc Engineering", slug: "acme-eng" },
  })
})

it("says what Cloud costs, in the cloud edition only", () => {
  vi.stubEnv("VITE_LOOMPLANE_EDITION", "cloud")
  render(<OrganizationForm onCreated={async () => {}} />)
  expect(
    screen.getByText(
      "$5 a month, including $5 of usage. You'll add a card next."
    )
  ).toBeInTheDocument()
  cleanup()
  vi.unstubAllEnvs()
  render(<OrganizationForm onCreated={async () => {}} />)
  expect(screen.queryByText(/including \$5 of usage/)).not.toBeInTheDocument()
})
