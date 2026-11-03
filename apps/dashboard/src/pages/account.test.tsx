import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { describeAgent } from "@/lib/account"
import { AccountProfile } from "./account-profile"
import { AccountSecurity } from "./account-security"
import { AccountSessions } from "./account-sessions"

const auth = vi.hoisted(() => ({
  client: {} as Record<string, ReturnType<typeof vi.fn>>,
  signOut: vi.fn(),
  refreshNow: vi.fn(),
  config: {} as Record<string, unknown>,
}))

vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({
    client: auth.client,
    signOut: auth.signOut,
    manager: { refreshNow: auth.refreshNow },
    clientConfig: auth.config,
  }),
  useSessionToken: () => null,
}))
const session = vi.hoisted(() => ({ id: "ses_here" as string | undefined }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", sessionId: session.id, userId: "u" },
    can: () => true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    ...rest
  }: { children: ReactNode; to: string } & Record<string, unknown>) => (
    <a {...rest} href={to}>
      {children}
    </a>
  ),
}))

const me = {
  id: "u",
  email: "rex@xraph.com",
  email_verified: true,
  first_name: "Rex",
  last_name: "Raphael",
  username: "rex",
  password_changed_at: "2026-07-01T10:00:00Z",
}

beforeEach(() => {
  auth.client = {
    getMe: vi.fn(async () => me),
    updateMe: vi.fn(async (body: object) => ({ ...me, ...body })),
    exportMyData: vi.fn(async () => ({ user: me })),
    deleteMe: vi.fn(async () => ({ status: "deleted" })),
    listSessions: vi.fn(async () => ({
      sessions: [
        {
          id: "ses_here",
          user_agent:
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36",
          ip_address: "203.0.113.4",
          created_at: "2026-11-01T10:00:00Z",
          last_activity_at: "2026-11-03T10:00:00Z",
        },
        {
          id: "ses_phone",
          user_agent:
            "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
          ip_address: "198.51.100.7",
          created_at: "2026-10-20T10:00:00Z",
          last_activity_at: "2026-11-02T10:00:00Z",
        },
      ],
    })),
    revokeSession: vi.fn(async () => ({ status: "revoked" })),
    revokeOtherSessions: vi.fn(async () => ({ status: "revoked" })),
    changePassword: vi.fn(async () => ({ status: "changed" })),
    listAuthMethods: vi.fn(async () => ({
      methods: [{ provider: "password", type: "password", label: "Password" }],
    })),
    unlinkAuthMethod: vi.fn(async () => ({})),
  }
  auth.config = {}
  session.id = "ses_here"
})

afterEach(() => vi.clearAllMocks())

describe("describeAgent", () => {
  it.each([
    [
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36",
      "Chrome on macOS",
    ],
    [
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0",
      "Edge on Windows",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
      "Safari on iPhone",
    ],
    ["", "Unknown device"],
  ])("names %s", (agent, label) => {
    expect(describeAgent(agent).label).toBe(label)
  })
})

describe("profile", () => {
  it("saves your name and username", async () => {
    render(<AccountProfile />)
    const first = await screen.findByLabelText("First name")
    expect(first).toHaveValue("Rex")
    await userEvent.clear(first)
    await userEvent.type(first, "Rexford")
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }))
    await waitFor(() =>
      expect(auth.client.updateMe).toHaveBeenCalledWith(
        { first_name: "Rexford", last_name: "Raphael", username: "rex" },
        ""
      )
    )
    // The sidebar shows the new name once the session reloads.
    expect(auth.refreshNow).toHaveBeenCalled()
    expect(await screen.findByText("Saved.")).toBeInTheDocument()
  })

  it("shows your email and whether it's verified", async () => {
    render(<AccountProfile />)
    expect(await screen.findByText("rex@xraph.com")).toBeInTheDocument()
    expect(screen.getByText("Verified")).toBeInTheDocument()
  })

  it("deletes the account only once you type your email", async () => {
    render(<AccountProfile />)
    await userEvent.click(
      await screen.findByRole("button", { name: "Delete account" })
    )
    const confirm = screen.getByRole("button", { name: "Delete for good" })
    expect(confirm).toBeDisabled()
    await userEvent.type(
      screen.getByLabelText("Type your email to confirm"),
      "rex@xraph.com"
    )
    await userEvent.click(confirm)
    await waitFor(() => expect(auth.client.deleteMe).toHaveBeenCalledWith(""))
    expect(auth.signOut).toHaveBeenCalled()
  })
})

describe("sessions", () => {
  it("marks this device and signs out another", async () => {
    render(<AccountSessions />)
    const here = (await screen.findByText("Chrome on macOS")).closest("li")!
    expect(within(here).getByText("This device")).toBeInTheDocument()
    expect(within(here).queryByRole("button")).toBeNull()

    const phone = screen.getByText("Safari on iPhone").closest("li")!
    await userEvent.click(
      within(phone).getByRole("button", { name: "Sign out" })
    )
    await waitFor(() =>
      expect(auth.client.revokeSession).toHaveBeenCalledWith("ses_phone", "")
    )
    await waitFor(() =>
      expect(screen.queryByText("Safari on iPhone")).toBeNull()
    )
  })

  it("offers no per-session sign-out when it can't tell which is this browser", async () => {
    session.id = undefined
    render(<AccountSessions />)
    await screen.findByText("Safari on iPhone")
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull()
    expect(
      screen.getByRole("button", { name: "Sign out everywhere else" })
    ).toBeEnabled()
  })

  it("signs out everywhere else", async () => {
    render(<AccountSessions />)
    await screen.findByText("Safari on iPhone")
    await userEvent.click(
      screen.getByRole("button", { name: "Sign out everywhere else" })
    )
    await waitFor(() =>
      expect(auth.client.revokeOtherSessions).toHaveBeenCalledWith("")
    )
  })
})

describe("security", () => {
  it("changes the password and signs out other sessions", async () => {
    render(<AccountSecurity />)
    await userEvent.click(
      await screen.findByRole("button", { name: "Change password" })
    )
    const dialog = await screen.findByRole("dialog")
    await userEvent.type(
      within(dialog).getByLabelText("Current password"),
      "old-secret"
    )
    await userEvent.type(
      within(dialog).getByLabelText("New password"),
      "a much longer secret"
    )
    await userEvent.type(
      within(dialog).getByLabelText("Type it again"),
      "a much longer secret"
    )
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Change password" })
    )
    await waitFor(() =>
      expect(auth.client.changePassword).toHaveBeenCalledWith(
        {
          current_password: "old-secret",
          new_password: "a much longer secret",
        },
        ""
      )
    )
    expect(auth.client.revokeOtherSessions).toHaveBeenCalledWith("")
    expect(await screen.findByText("Password changed")).toBeInTheDocument()
  })

  it("won't send a password typed differently twice", async () => {
    render(<AccountSecurity />)
    await userEvent.click(
      await screen.findByRole("button", { name: "Change password" })
    )
    const dialog = await screen.findByRole("dialog")
    await userEvent.type(within(dialog).getByLabelText("Current password"), "x")
    await userEvent.type(
      within(dialog).getByLabelText("New password"),
      "a much longer secret"
    )
    await userEvent.type(
      within(dialog).getByLabelText("Type it again"),
      "a much longer secreT"
    )
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Change password" })
    )
    expect(
      await within(dialog).findByText("The two new passwords don't match.")
    ).toBeInTheDocument()
    expect(auth.client.changePassword).not.toHaveBeenCalled()
  })

  it("lists how you sign in and keeps the last one", async () => {
    auth.client.listAuthMethods = vi.fn(async () => ({
      methods: [
        { provider: "password", type: "password", label: "Password" },
        {
          provider: "github",
          type: "social",
          label: "GitHub",
          linked_at: "2026-10-01T10:00:00Z",
        },
      ],
    }))
    render(<AccountSecurity />)
    const methods = await screen.findByRole("region", {
      name: "Sign-in methods",
    })
    const github = (await within(methods).findByText("GitHub")).closest("li")!
    await userEvent.click(
      within(github).getByRole("button", { name: "Disconnect" })
    )
    await waitFor(() =>
      expect(auth.client.unlinkAuthMethod).toHaveBeenCalledWith("github", "")
    )
    // Password is the only way in now, so it can't be removed.
    const password = within(methods)
      .getByText("Email and password")
      .closest("li")!
    expect(within(password).queryByRole("button")).toBeNull()
  })
})
