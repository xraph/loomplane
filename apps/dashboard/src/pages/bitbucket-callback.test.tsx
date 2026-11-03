import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { BitbucketCallback } from "@/pages/bitbucket-callback"

const navigate = vi.fn()
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => navigate,
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  navigate.mockReset()
})

type Sent = { method: string; path: string; body: unknown }

function stub(sent: Sent[], fail?: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        sent.push({
          method: "POST",
          path,
          body: JSON.parse(String(init.body ?? "{}")),
        })
        if (fail) return Response.json({ message: fail }, { status: 400 })
        return Response.json({ id: "c1", workspace: "acme", name: "Acme Inc" })
      }
      return Response.json({})
    })
  )
}

const show = (props: { code?: string; state?: string; error?: string }) =>
  render(
    <ClientProvider client={forgeClient}>
      <BitbucketCallback code="" state="" error="" {...props} />
    </ClientProvider>
  )

it("posts what Bitbucket sent, then goes back to Integrations", async () => {
  const sent: Sent[] = []
  stub(sent)
  show({ code: "c0de", state: "st4te" })
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ to: "/integrations" })
    )
  )
  expect(sent).toEqual([
    {
      method: "POST",
      path: "/api/v1/bitbucket/workspaces",
      body: { code: "c0de", state: "st4te" },
    },
  ])
})

it("posts once", async () => {
  const sent: Sent[] = []
  stub(sent)
  const { rerender } = show({ code: "a", state: "b" })
  rerender(
    <ClientProvider client={forgeClient}>
      <BitbucketCallback code="a" state="b" error="" />
    </ClientProvider>
  )
  await waitFor(() => expect(navigate).toHaveBeenCalled())
  expect(sent).toHaveLength(1)
})

it("shows Bitbucket's refusal and posts nothing", async () => {
  const sent: Sent[] = []
  stub(sent)
  show({ error: "access_denied", state: "st4te" })
  expect(
    await screen.findByText(/Bitbucket didn.t connect: access_denied/)
  ).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Back to Integrations" })
  ).toHaveAttribute("href", "/integrations")
  expect(sent).toEqual([])
  expect(navigate).not.toHaveBeenCalled()
})

it("shows the server's message when connecting fails", async () => {
  stub([], "That sign-in expired")
  show({ code: "c0de", state: "st4te" })
  expect(await screen.findByText("That sign-in expired.")).toBeInTheDocument()
  expect(
    screen.getByRole("link", { name: "Back to Integrations" })
  ).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})

it("says so when Bitbucket sent nothing back", async () => {
  const sent: Sent[] = []
  stub(sent)
  show({})
  expect(
    await screen.findByText(/Bitbucket didn.t send a sign-in back/)
  ).toBeInTheDocument()
  expect(sent).toEqual([])
})
