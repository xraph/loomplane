import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { CardGate } from "./card-gate"

const state = vi.hoisted(() => ({
  path: "/projects",
  status: "pending_payment" as string | undefined,
  error: undefined as unknown,
  owner: true,
  assigned: [] as string[],
}))

vi.mock("@/lib/router", () => ({ usePath: () => state.path }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => state.owner }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("./client", () => ({
  useBillingStatus: () => ({
    data: state.status ? { status: state.status } : undefined,
    error: state.error,
    loading: false,
    refetch: () => {},
  }),
  useSetupSession: () => ({
    mutateAsync: async () => ({ url: "https://stripe.test/setup" }),
    isPending: false,
  }),
}))

beforeEach(() => {
  state.path = "/projects"
  state.status = "pending_payment"
  state.error = undefined
  state.owner = true
  state.assigned = []
  vi.stubGlobal("location", {
    ...window.location,
    assign: (url: string) => state.assigned.push(url),
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const gate = () =>
  render(
    <CardGate>
      <p>projects</p>
    </CardGate>
  )

it("blocks a project page while pending and lets billing through", async () => {
  const view = gate()
  expect(
    await screen.findByText("Add a card to start using Loomplane Cloud")
  ).toBeInTheDocument()
  expect(screen.queryByText("projects")).not.toBeInTheDocument()
  state.path = "/settings/billing"
  view.rerender(
    <CardGate>
      <p>projects</p>
    </CardGate>
  )
  expect(screen.getByText("projects")).toBeInTheDocument()
})

it("lets connecting a provider through while pending", () => {
  for (const path of [
    "/integrations",
    "/integrations/connect/aws",
    "/clouds/accounts/x",
    "/members",
    "/teams",
    "/api-keys",
    "/account/profile",
    "/notifications/settings",
    "/platform/cloud",
  ]) {
    state.path = path
    gate()
    expect(screen.getByText("projects")).toBeInTheDocument()
    cleanup()
  }
})

it("blocks the pages that run things", () => {
  for (const path of [
    "/",
    "/deployments",
    "/clouds",
    "/services",
    "/members/x",
  ]) {
    state.path = path
    gate()
    expect(screen.queryByText("projects")).not.toBeInTheDocument()
    cleanup()
  }
})

it("lets the owner start adding a card", async () => {
  gate()
  await userEvent.click(
    await screen.findByRole("button", { name: "Add a card" })
  )
  await waitFor(() =>
    expect(state.assigned).toEqual(["https://stripe.test/setup"])
  )
})

it("tells a member who isn't the owner to ask the owner", async () => {
  state.owner = false
  gate()
  expect(
    await screen.findByText(
      "The workspace owner needs to add a card before anything runs here."
    )
  ).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Add a card" })).toBeNull()
})

it("renders the page on a 404 from the status route", () => {
  state.status = undefined
  state.error = Object.assign(new Error("not found"), { statusCode: 404 })
  gate()
  expect(screen.getByText("projects")).toBeInTheDocument()
})

it("renders the page for a workspace that pays", () => {
  state.status = "active"
  gate()
  expect(screen.getByText("projects")).toBeInTheDocument()
})

it("leaves a past due workspace's pages to the server", () => {
  state.status = "past_due"
  gate()
  expect(screen.getByText("projects")).toBeInTheDocument()
})
