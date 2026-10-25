import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { StrictMode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { OAuthCallback } from "@/pages/oauth-callback"

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

function stub() {
  const fetcher = vi.fn<typeof fetch>(async () =>
    Response.json(
      {
        id: "conn_do",
        name: "Team DO",
        cloud: "digitalocean",
        status: "ready",
      },
      { status: 201 }
    )
  )
  vi.stubGlobal("fetch", fetcher)
  return fetcher
}

function page(props: Partial<Parameters<typeof OAuthCallback>[0]>) {
  render(
    <StrictMode>
      <ClientProvider client={forgeClient}>
        <OAuthCallback
          state="s1"
          code="c1"
          error=""
          errorDescription=""
          {...props}
        />
      </ClientProvider>
    </StrictMode>
  )
}

it("completes a sign-in once and opens the connection", async () => {
  const fetcher = stub()
  page({})
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/clouds/accounts/$id",
      params: { id: "conn_do" },
    })
  )
  const posts = fetcher.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(posts).toHaveLength(1)
  expect(JSON.parse(String(posts[0]![1]?.body))).toEqual({
    state: "s1",
    code: "c1",
  })
})

it("shows the cloud's error and a way back", async () => {
  const fetcher = stub()
  page({ code: "", error: "access_denied", errorDescription: "You said no." })
  expect(await screen.findByText("You said no.")).toBeTruthy()
  expect(
    screen
      .getByRole("link", { name: "Back to integrations" })
      .getAttribute("href")
  ).toBe("/integrations")
  expect(fetcher).not.toHaveBeenCalled()
  expect(navigate).not.toHaveBeenCalled()
})
