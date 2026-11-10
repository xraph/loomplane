import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setInstance } from "@/lib/instance"
import { PlatformFlags } from "@/pages/platform-flags"

vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/route-feedback", () => ({
  PageNotFound: () => <h1>Page not found</h1>,
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
  })
})

const open = {
  id: "aflag_1",
  org_id: "org_acme",
  project_id: "proj_1",
  conversation_id: "conv_1",
  task_id: "atask_1",
  reported_by: "usr_1",
  reason: "harmful",
  note: "It told me to drop the database",
  question: "How do I clear staging?",
  answer: "Run DROP DATABASE on it.",
  created_at: "2026-11-03T10:00:00Z",
  updated_at: "2026-11-03T10:00:00Z",
  dismissed_by: "",
  dismissed_at: null,
}

function admin() {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
    platformOwner: true,
  })
}

function serve(status = 200) {
  const calls: { method: string; url: URL }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      calls.push({ method: init?.method ?? "GET", url })
      if (status !== 200)
        return Response.json({ error: "not found" }, { status })
      if (init?.method === "POST")
        return Response.json({
          ...open,
          dismissed_by: "usr_owner",
          dismissed_at: "2026-11-04T10:00:00Z",
        })
      return Response.json({ flags: [open] })
    })
  )
  return calls
}

function mount() {
  render(
    <ClientProvider client={forgeClient}>
      <PlatformFlags />
    </ClientProvider>
  )
}

it("lists a flag with its reason, note, question and answer", async () => {
  admin()
  const calls = serve()
  mount()
  expect(
    await screen.findByText("It told me to drop the database")
  ).toBeTruthy()
  expect(screen.getByText("Harmful")).toBeTruthy()
  expect(screen.getByText("How do I clear staging?")).toBeTruthy()
  expect(screen.getByText("Run DROP DATABASE on it.")).toBeTruthy()
  expect(screen.getByText("org_acme")).toBeTruthy()
  expect(calls[0].url.searchParams.get("status")).toBe("open")
})

it("reads the other filters from the server", async () => {
  admin()
  const calls = serve()
  mount()
  await screen.findByText("Run DROP DATABASE on it.")
  await userEvent.click(screen.getByRole("tab", { name: "Dismissed" }))
  await screen.findByText("Run DROP DATABASE on it.")
  expect(calls.at(-1)?.url.searchParams.get("status")).toBe("dismissed")
})

it("dismisses a flag", async () => {
  admin()
  const calls = serve()
  mount()
  const row = (await screen.findByText("Run DROP DATABASE on it.")).closest(
    "li"
  ) as HTMLElement
  await userEvent.click(within(row).getByRole("button", { name: "Dismiss" }))
  await vi.waitFor(() =>
    expect(
      calls.some(
        (c) =>
          c.method === "POST" &&
          c.url.pathname === "/api/v1/platform/agent/flags/aflag_1/dismiss"
      )
    ).toBe(true)
  )
})

it("shows an admin who isn't the owner the not-found view", () => {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin: true,
  })
  mount()
  expect(screen.getByText("Page not found")).toBeTruthy()
})

it("shows a dismissed report's date and no Dismiss button", async () => {
  admin()
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        flags: [
          {
            ...open,
            dismissed_by: "usr_owner",
            dismissed_at: "2026-11-04T10:00:00Z",
          },
        ],
      })
    )
  )
  mount()
  expect(await screen.findByText(/^Dismissed /)).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull()
})

it("shows anyone else the not-found view", () => {
  mount()
  expect(screen.getByText("Page not found")).toBeTruthy()
})

it("treats a 404 from the server as not for you", async () => {
  admin()
  serve(404)
  mount()
  expect(await screen.findByText("Page not found")).toBeTruthy()
})
