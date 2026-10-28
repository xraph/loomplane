import { ClientProvider } from "@forge-go/client-react"
import {
  fireEvent,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Notifications } from "@/pages/notifications"

const organization = vi.hoisted(() => ({ admin: true, orgId: "org-a" }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: organization.orgId },
    can: (permission: string) =>
      permission === "member:manage" && organization.admin,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  organization.admin = true
  organization.orgId = "org-a"
})

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const notification = {
  id: "notice-a",
  title: "Deployment succeeded",
  body: "Production is ready.",
  action_url: "/deployments/deploy-a",
  read: false,
  created_at: "2026-10-18T12:00:00Z",
  project_id: "project-a",
  status: "succeeded",
  email_status: "sent",
}
const endpoint = {
  id: "hook-a",
  url: "https://example.com/loomplane",
  description: "Release automation",
  enabled: true,
  created_at: "2026-10-18T12:00:00Z",
}

function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <Notifications />
    </ClientProvider>
  )
}

function installAPI(
  handler?: (path: string, init?: RequestInit) => Response | undefined
) {
  const requests: { path: string; init?: RequestInit }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      requests.push({ path, init })
      const overridden = handler?.(path, init)
      if (overridden) return overridden
      if (path.endsWith("/notifications/preferences"))
        return response({ deployment_email: true, mail_enabled: true })
      if (path.endsWith("/notifications/webhooks"))
        return response({ items: [] })
      if (path.endsWith("/notifications"))
        return response({ items: [], mail_enabled: true })
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`)
    }
  )
  return requests
}

it("keeps a failed inbox distinct from an empty inbox and retries the read", async () => {
  let failed = true
  installAPI((path) =>
    path.endsWith("/notifications") && failed
      ? response({ error: "Inbox unavailable" }, 503)
      : undefined
  )
  mount()
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Inbox unavailable"
  )
  expect(screen.queryByText("No notifications yet")).not.toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry inbox" }))
  expect(await screen.findByText("No notifications yet")).toBeInTheDocument()
})

it("does not fetch or offer webhook administration without the organization grant", async () => {
  organization.admin = false
  const requests = installAPI()
  mount()
  expect(await screen.findByText("No notifications yet")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Add webhook" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Retry notification processing" })
  ).not.toBeInTheDocument()
  expect(requests.some(({ path }) => path.includes("/webhooks"))).toBe(false)
})

it("allows an administrator to retry exhausted notification processing", async () => {
  installAPI((path, init) =>
    path.endsWith("/notifications/retry") && init?.method === "POST"
      ? response({ ok: true })
      : undefined
  )
  mount()
  await userEvent.click(
    screen.getByRole("button", { name: "Retry notification processing" })
  )
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Failed notification processing is queued for this workspace."
  )
})

it("shows disabled mail without discarding a saved email preference", async () => {
  installAPI((path) =>
    path.endsWith("/preferences")
      ? response({ deployment_email: true, mail_enabled: false })
      : undefined
  )
  mount()
  expect(
    await screen.findByText(/Email delivery is disabled/)
  ).toBeInTheDocument()
  const preference = screen.getByRole("switch", { name: "Deployment email" })
  expect(preference).toBeChecked()
  expect(preference).not.toBeDisabled()
})

it("shows a failed preference save and keeps the persisted preference", async () => {
  installAPI((path, init) =>
    path.endsWith("/preferences") && init?.method === "PUT"
      ? response({ error: "Preferences unavailable" }, 503)
      : undefined
  )
  mount()
  const preference = await screen.findByRole("switch", {
    name: "Deployment email",
  })
  await userEvent.click(preference)
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Preferences unavailable"
  )
  expect(preference).toBeChecked()
})

it("persists opting out and back in through the generated request codec", async () => {
  let enabled = true
  installAPI((path, init) => {
    if (!path.endsWith("/preferences")) return undefined
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>
      if (typeof body.deployment_email !== "boolean")
        return response({ error: "deployment_email is required" }, 400)
      enabled = body.deployment_email
    }
    return response({ deployment_email: enabled, mail_enabled: true })
  })
  mount()
  const preference = await screen.findByRole("switch", {
    name: "Deployment email",
  })
  expect(preference).toBeChecked()
  await userEvent.click(preference)
  await waitFor(() => expect(preference).not.toBeChecked())
  await userEvent.click(preference)
  await waitFor(() => expect(preference).toBeChecked())
})

it("marks a notification read only after the server accepts the change", async () => {
  let read = false
  installAPI((path, init) => {
    if (path.endsWith("/notice-a/read") && init?.method === "PUT") {
      read = true
      return response({ ok: true })
    }
    if (path.endsWith("/notifications"))
      return response({
        items: [{ ...notification, read }],
        mail_enabled: true,
      })
  })
  mount()
  expect(
    await screen.findByRole("link", { name: "View deployment" })
  ).toHaveAttribute("href", "/deployments/deploy-a")
  await userEvent.click(screen.getByRole("button", { name: "Mark as read" }))
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "Mark as read" })
    ).not.toBeInTheDocument()
  )
})

it("refreshes the inbox when the page regains focus", async () => {
  let complete = false
  installAPI((path) =>
    path.endsWith("/notifications")
      ? response({ items: complete ? [notification] : [], mail_enabled: true })
      : undefined
  )
  mount()
  expect(await screen.findByText("No notifications yet")).toBeInTheDocument()
  complete = true
  fireEvent.focus(window)
  expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
})

it.each([401, 403])(
  "hides cached notifications and controls after a %s refresh",
  async (status) => {
    let denied = false
    let failureStatus = status
    installAPI((path) => {
      if (denied)
        return response(
          {
            error:
              failureStatus === 503
                ? "Temporarily unavailable"
                : "Access denied",
          },
          failureStatus
        )
      if (path.endsWith("/notifications"))
        return response({ items: [notification], mail_enabled: true })
      if (path.endsWith("/webhooks")) return response({ items: [endpoint] })
    })
    mount()
    expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
    expect(
      await screen.findByRole("button", { name: "Delivery history" })
    ).toBeInTheDocument()
    expect(
      screen.getByRole("switch", { name: "Deployment email" })
    ).toBeChecked()

    denied = true
    fireEvent.focus(window)
    await waitFor(() => {
      expect(screen.getAllByRole("alert").length).toBeGreaterThanOrEqual(2)
      expect(screen.queryByText("Deployment succeeded")).not.toBeInTheDocument()
      expect(screen.queryByRole("switch")).not.toBeInTheDocument()
      expect(
        screen.queryByRole("link", { name: "View deployment" })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole("button", { name: "Mark as read" })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole("button", { name: "Add webhook" })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole("button", { name: "Delivery history" })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole("button", { name: "Retry notification processing" })
      ).not.toBeInTheDocument()
    })

    failureStatus = 503
    fireEvent.focus(window)
    await waitFor(
      () => {
        expect(screen.getAllByText(/Temporarily unavailable/)).toHaveLength(2)
      },
      { timeout: 5000 }
    )
    expect(screen.queryByText("Deployment succeeded")).not.toBeInTheDocument()
    expect(screen.queryByRole("switch")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Add webhook" })
    ).not.toBeInTheDocument()

    denied = false
    fireEvent.focus(window)
    expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
    expect(
      await screen.findByRole("switch", { name: "Deployment email" })
    ).toBeChecked()
    await userEvent.click(
      await screen.findByRole("button", { name: "Retry webhooks" })
    )
    expect(
      await screen.findByRole("button", { name: "Add webhook" })
    ).toBeInTheDocument()
  }
)

it.each([401, 403])(
  "hides cached delivery history after a %s refresh",
  async (status) => {
    let denied = false
    installAPI((path) => {
      if (path.endsWith("/webhooks")) return response({ items: [endpoint] })
      if (path.endsWith("/deliveries"))
        return denied
          ? response({ error: "Access denied" }, status)
          : response({
              items: [
                {
                  id: "delivery-a",
                  event_type: "deployment.failed",
                  status: "pending",
                  attempts: 2,
                  status_code: 503,
                  created_at: "2026-10-18T12:00:00Z",
                },
              ],
            })
    })
    mount()
    await userEvent.click(
      await screen.findByRole("button", { name: "Delivery history" })
    )
    const dialog = screen.getByRole("dialog")
    expect(
      await within(dialog).findByText("deployment.failed")
    ).toBeInTheDocument()
    denied = true
    fireEvent.focus(window)
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Access denied"
    )
    expect(
      within(dialog).queryByText("deployment.failed")
    ).not.toBeInTheDocument()
    expect(within(dialog).queryByText("HTTP 503")).not.toBeInTheDocument()
    expect(within(dialog).queryByText(endpoint.url)).not.toBeInTheDocument()
  }
)

it.each([401, 403])(
  "hides an open signing secret after a %s webhook refresh",
  async (status) => {
    let denied = false
    let failureStatus = status
    installAPI((path, init) => {
      if (path.endsWith("/webhooks") && init?.method === "POST")
        return response({ endpoint, secret: "whsec_revoked_access" }, 201)
      if (path.endsWith("/webhooks"))
        return denied
          ? response(
              {
                error:
                  failureStatus === 503
                    ? "Temporarily unavailable"
                    : "Access denied",
              },
              failureStatus
            )
          : response({ items: [endpoint] })
    })
    mount()
    await userEvent.click(
      await screen.findByRole("button", { name: "Add webhook" })
    )
    await userEvent.type(
      screen.getByRole("textbox", { name: "Endpoint URL" }),
      endpoint.url
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Create webhook" })
    )
    expect(
      await screen.findByDisplayValue("whsec_revoked_access")
    ).toBeInTheDocument()
    denied = true
    fireEvent.focus(window)
    expect(await screen.findByRole("alert")).toHaveTextContent("Access denied")
    expect(
      screen.queryByDisplayValue("whsec_revoked_access")
    ).not.toBeInTheDocument()
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Add webhook" })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Retry notification processing" })
    ).not.toBeInTheDocument()

    failureStatus = 503
    fireEvent.focus(window)
    await screen.findByText(/Temporarily unavailable/, {}, { timeout: 5000 })
    expect(screen.queryByText(endpoint.url)).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Add webhook" })
    ).not.toBeInTheDocument()

    denied = false
    fireEvent.focus(window)
    expect(
      await screen.findByRole("button", { name: "Add webhook" })
    ).toBeInTheDocument()
    expect(
      screen.queryByDisplayValue("whsec_revoked_access")
    ).not.toBeInTheDocument()
  }
)

it("closes rotation confirmation before showing the new signing secret", async () => {
  installAPI((path, init) => {
    if (path.endsWith("/rotate") && init?.method === "POST")
      return response({ endpoint, secret: "whsec_rotated_test" })
    if (path.endsWith("/webhooks")) return response({ items: [endpoint] })
  })
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Rotate secret" })
  )
  await userEvent.click(
    within(screen.getByRole("alertdialog")).getByRole("button", {
      name: "Rotate secret",
    })
  )
  expect(await screen.findByDisplayValue("whsec_rotated_test")).toBeVisible()
  expect(
    screen.queryByRole("alertdialog", { hidden: true })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Done" }))
  expect(screen.getByRole("button", { name: "Delete" })).toBeVisible()
})

it("closes deletion confirmation after removing a webhook", async () => {
  let removed = false
  installAPI((path, init) => {
    if (path.endsWith("/hook-a") && init?.method === "DELETE") {
      removed = true
      return response({ ok: true })
    }
    if (path.endsWith("/webhooks"))
      return response({ items: removed ? [] : [endpoint] })
  })
  mount()
  await userEvent.click(await screen.findByRole("button", { name: "Delete" }))
  await userEvent.click(
    within(screen.getByRole("alertdialog")).getByRole("button", {
      name: "Delete webhook",
    })
  )
  expect(await screen.findByText("No webhooks configured")).toBeVisible()
  expect(
    screen.queryByRole("alertdialog", { hidden: true })
  ).not.toBeInTheDocument()
})

it("retains cached notification content during transient refresh failures", async () => {
  let failed = false
  installAPI((path) => {
    if (failed) return response({ error: "Temporarily unavailable" }, 503)
    if (path.endsWith("/notifications"))
      return response({ items: [notification], mail_enabled: true })
    if (path.endsWith("/webhooks")) return response({ items: [endpoint] })
    if (path.endsWith("/deliveries"))
      return response({
        items: [
          {
            id: "delivery-a",
            event_type: "deployment.failed",
            status: "pending",
            attempts: 2,
            status_code: 503,
            created_at: "2026-10-18T12:00:00Z",
          },
        ],
      })
  })
  mount()
  expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
  await userEvent.click(
    await screen.findByRole("button", { name: "Delivery history" })
  )
  const dialog = screen.getByRole("dialog")
  expect(
    await within(dialog).findByText("deployment.failed")
  ).toBeInTheDocument()
  failed = true
  fireEvent.focus(window)
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "Temporarily unavailable"
  )
  expect(screen.getByText("Deployment succeeded")).toBeInTheDocument()
  expect(
    screen.getByRole("switch", { name: "Deployment email", hidden: true })
  ).toBeChecked()
  expect(
    screen.getByRole("button", { name: "Add webhook", hidden: true })
  ).toBeInTheDocument()
  expect(within(dialog).getByText("deployment.failed")).toBeInTheDocument()
})

it("keeps a failed email delivery separate from a successful deployment", async () => {
  installAPI((path) =>
    path.endsWith("/notifications")
      ? response({
          items: [{ ...notification, email_status: "failed" }],
          mail_enabled: true,
        })
      : undefined
  )
  mount()
  expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
  expect(screen.getByText("Email failed")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "View deployment" })).toHaveAttribute(
    "href",
    "/deployments/deploy-a"
  )
})

it("does not render executable notification links", async () => {
  installAPI((path) =>
    path.endsWith("/notifications")
      ? response({
          items: [{ ...notification, action_url: "javascript:alert(1)" }],
          mail_enabled: true,
        })
      : undefined
  )
  mount()
  expect(await screen.findByText("Deployment succeeded")).toBeInTheDocument()
  expect(
    screen.queryByRole("link", { name: "View deployment" })
  ).not.toBeInTheDocument()
})

it("clears the one-time signing secret when its dialog closes", async () => {
  let created = false
  installAPI((path, init) => {
    if (path.endsWith("/webhooks") && init?.method === "POST") {
      created = true
      return response({ endpoint, secret: "whsec_one_time_only" }, 201)
    }
    if (path.endsWith("/webhooks"))
      return response({ items: created ? [endpoint] : [] })
  })
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Add webhook" })
  )
  const dialog = screen.getByRole("dialog")
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Endpoint URL" }),
    endpoint.url
  )
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Description" }),
    endpoint.description
  )
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Create webhook" })
  )
  expect(
    await screen.findByDisplayValue("whsec_one_time_only")
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Done" }))
  await waitFor(() =>
    expect(
      screen.queryByDisplayValue("whsec_one_time_only")
    ).not.toBeInTheDocument()
  )
  await userEvent.click(screen.getByRole("button", { name: "Add webhook" }))
  expect(
    screen.queryByDisplayValue("whsec_one_time_only")
  ).not.toBeInTheDocument()
  expect(
    within(screen.getByRole("dialog")).getByRole("textbox", {
      name: "Endpoint URL",
    })
  ).toHaveValue("")
})

it("clears the one-time signing secret when the organization changes", async () => {
  installAPI((path, init) =>
    path.endsWith("/webhooks") && init?.method === "POST"
      ? response({ endpoint, secret: "whsec_org_a_only" }, 201)
      : undefined
  )
  const view = mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Add webhook" })
  )
  await userEvent.type(
    screen.getByRole("textbox", { name: "Endpoint URL" }),
    endpoint.url
  )
  await userEvent.click(screen.getByRole("button", { name: "Create webhook" }))
  expect(
    await screen.findByDisplayValue("whsec_org_a_only")
  ).toBeInTheDocument()
  organization.orgId = "org-b"
  view.rerender(
    <ClientProvider client={forgeClient}>
      <Notifications />
    </ClientProvider>
  )
  await waitFor(() =>
    expect(
      screen.queryByDisplayValue("whsec_org_a_only")
    ).not.toBeInTheDocument()
  )
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
})

it("shows delivery failures and their retry schedule without exposing request payloads", async () => {
  installAPI((path) => {
    if (path.endsWith("/webhooks")) return response({ items: [endpoint] })
    if (path.endsWith("/deliveries"))
      return response({
        items: [
          {
            id: "delivery-a",
            event_type: "deployment.failed",
            status: "pending",
            attempts: 2,
            status_code: 503,
            created_at: "2026-10-18T12:00:00Z",
            next_retry_at: "2026-10-18T12:10:00Z",
          },
        ],
      })
  })
  mount()
  await userEvent.click(
    await screen.findByRole("button", { name: "Delivery history" })
  )
  const dialog = screen.getByRole("dialog")
  expect(
    await within(dialog).findByText("deployment.failed")
  ).toBeInTheDocument()
  expect(within(dialog).getByText("Retrying")).toBeInTheDocument()
  expect(within(dialog).getByText(/2 attempts/)).toBeInTheDocument()
  expect(within(dialog).getByText("HTTP 503")).toBeInTheDocument()
  expect(within(dialog).getByText(/Next retry/)).toBeInTheDocument()
})
