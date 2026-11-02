import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { NotificationBell } from "@/components/notification-bell"

const navigate = vi.hoisted(() => vi.fn())
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org-a" } }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ to, ...props }: { to: string }) => <a href={to} {...props} />,
  useNavigate: () => navigate,
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  navigate.mockReset()
})

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const notice = (id: string, read: boolean) => ({
  id,
  title: `Deployment ${id} succeeded`,
  body: "Production is ready.",
  action_url: `/deployments/${id}`,
  read,
  created_at: "2026-10-18T12:00:00Z",
  project_id: "project-a",
  status: "succeeded",
  email_status: "sent",
})

function installAPI(items: ReturnType<typeof notice>[]) {
  const reads: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const read = path.match(/\/notifications\/([^/]+)\/read$/)
      if (read && init?.method === "PUT") {
        reads.push(read[1])
        return response({ ok: true })
      }
      if (path.endsWith("/notifications"))
        return response({
          items: items.map((n) => ({
            ...n,
            read: n.read || reads.includes(n.id),
          })),
          mail_enabled: true,
        })
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`)
    }
  )
  return reads
}

function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <NotificationBell />
    </ClientProvider>
  )
}

it("says how many notifications are unread and links to the full inbox and its settings", async () => {
  installAPI([notice("a", false), notice("b", false), notice("c", true)])
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Notifications, 2 unread" })
  )
  expect(await screen.findByText("Deployment a succeeded")).toBeVisible()
  expect(screen.getByText("Deployment c succeeded")).toBeVisible()
  expect(
    screen.getByRole("link", { name: "View all notifications" })
  ).toHaveAttribute("href", "/notifications")
  expect(
    screen.getByRole("link", { name: "Notification settings" })
  ).toHaveAttribute("href", "/notifications/settings")
})

it("marks a notice read and opens its deployment when you pick it", async () => {
  const reads = installAPI([notice("a", false)])
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Notifications, 1 unread" })
  )
  await userEvent.click(
    await screen.findByRole("button", { name: /Deployment a succeeded/ })
  )
  expect(navigate).toHaveBeenCalledWith({ href: "/deployments/a" })
  await waitFor(() => expect(reads).toEqual(["a"]))
  expect(
    await screen.findByRole("button", { name: "Notifications" })
  ).toBeInTheDocument()
})

it("shows only the latest notices and leaves the rest to the inbox", async () => {
  installAPI(Array.from({ length: 9 }, (_, i) => notice(String(i), true)))
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Notifications" })
  )
  expect(await screen.findByText("Deployment 0 succeeded")).toBeVisible()
  expect(screen.getByText("Deployment 5 succeeded")).toBeVisible()
  expect(screen.queryByText("Deployment 6 succeeded")).not.toBeInTheDocument()
})

it("does not navigate to an executable action", async () => {
  installAPI([{ ...notice("a", true), action_url: "javascript:alert(1)" }])
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Notifications" })
  )
  await userEvent.click(
    await screen.findByRole("button", { name: /Deployment a succeeded/ })
  )
  expect(navigate).not.toHaveBeenCalled()
})
