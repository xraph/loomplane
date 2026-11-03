import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { isUnprefixed } from "@/lib/workspace-url"
import { Device, formatCode } from "@/pages/device"

vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: { email: "ada@example.com" }, signOut: vi.fn() }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

type Sent = { path: string; method: string; body: unknown }

function stubComplete(answer: () => Response) {
  const sent: Sent[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push({
        path: new URL(String(input), "http://localhost").pathname,
        method: init?.method ?? "GET",
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      return answer()
    }
  )
  return sent
}

function renderDevice(code: string) {
  render(
    <ClientProvider client={forgeClient}>
      <Device code={code} />
    </ClientProvider>
  )
}

test("reads a code however it was typed", () => {
  expect(formatCode("bcdfghjk")).toBe("BCDF-GHJK")
  expect(formatCode(" bcdf-ghjk ")).toBe("BCDF-GHJK")
  expect(formatCode("bcd")).toBe("BCD")
  expect(formatCode("BCDF-GHJK-EXTRA")).toBe("BCDF-GHJK")
})

test("approves the code from the link only when you say so", async () => {
  const sent = stubComplete(() => Response.json({ status: "authorized" }))
  renderDevice("bcdf-ghjk")
  expect(screen.getByLabelText("The code loomctl showed you")).toHaveValue(
    "BCDF-GHJK"
  )
  // The onboarding frame: who's signed in, and signing out, above the card.
  expect(screen.getByText("ada@example.com")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument()
  // Opening the link sends nothing.
  expect(sent).toHaveLength(0)

  await userEvent.click(screen.getByRole("button", { name: "Approve" }))
  expect(
    await screen.findByRole("heading", { name: "loomctl is signed in" })
  ).toBeInTheDocument()
  expect(sent).toEqual([
    {
      path: "/authsome/v1/oauth/device/complete",
      method: "POST",
      body: { user_code: "BCDF-GHJK", action: "approve" },
    },
  ])
})

test("denies, and waits for a whole code before either", async () => {
  const sent = stubComplete(() => Response.json({ status: "denied" }))
  renderDevice("")
  const approve = screen.getByRole("button", { name: "Approve" })
  const deny = screen.getByRole("button", { name: "Deny" })
  expect(approve).toBeDisabled()
  await userEvent.type(
    screen.getByLabelText("The code loomctl showed you"),
    "bcdfghjk"
  )
  expect(approve).toBeEnabled()
  await userEvent.click(deny)
  expect(
    await screen.findByRole("heading", { name: "Sign-in denied" })
  ).toBeInTheDocument()
  expect(sent[0].body).toEqual({ user_code: "BCDF-GHJK", action: "deny" })
})

test("says when a code is unknown or expired", async () => {
  stubComplete(() =>
    Response.json({ message: "invalid or expired user code" }, { status: 404 })
  )
  renderDevice("BCDF-GHJK")
  await userEvent.click(screen.getByRole("button", { name: "Approve" }))
  expect(
    await screen.findByText(/isn't valid, or it expired/)
  ).toBeInTheDocument()
})

test("keeps one address whichever workspace you're in", () => {
  expect(isUnprefixed("/device")).toBe(true)
})
