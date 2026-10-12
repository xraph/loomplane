import { afterEach, describe, expect, it } from "vitest"

import { captureInvite, clearInvite, pendingInvite } from "@/lib/invite"
import { grantable, memberRoleFor, roleLabel, roles } from "@/lib/roles"

describe("memberRoleFor", () => {
  // Mirrors TestMemberRoleFor_roundTrips in internal/security: authsome
  // calls a developer a "member", and the role change endpoint wants that.
  it.each([
    ["owner", "owner"],
    ["admin", "admin"],
    ["developer", "member"],
    ["viewer", "viewer"],
  ] as const)("%s is authsome's %s", (role, want) => {
    expect(memberRoleFor(role)).toBe(want)
  })
})

describe("grantable", () => {
  const values = (role: string) => grantable(role).map((r) => r.value)

  it("lets an owner give every role", () => {
    expect(values("owner")).toEqual(roles.map((r) => r.value))
  })

  it("never offers a role above the caller's own", () => {
    expect(values("admin")).toEqual(["admin", "developer", "viewer"])
    expect(values("developer")).toEqual(["developer", "viewer"])
    expect(values("viewer")).toEqual(["viewer"])
  })

  it("treats a role it doesn't know as the lowest", () => {
    expect(values("auditor")).toEqual(["viewer"])
  })
})

describe("roleLabel", () => {
  it("names known roles and passes others through", () => {
    expect(roleLabel("developer")).toBe("Developer")
    expect(roleLabel("auditor")).toBe("auditor")
  })
})

describe("captureInvite", () => {
  afterEach(() => clearInvite())

  const at = (url: string) =>
    new URL(url, "http://localhost") as unknown as Location

  it("keeps the token from an invitation link", () => {
    captureInvite(at("/invitations/accept?token=abc%2B123"))
    expect(pendingInvite()).toBe("abc+123")
  })

  it("ignores tokens on any other page", () => {
    captureInvite(at("/projects?token=abc"))
    expect(pendingInvite()).toBeNull()
  })

  it("leaves an earlier invitation alone when the link has no token", () => {
    captureInvite(at("/invitations/accept?token=first"))
    captureInvite(at("/invitations/accept"))
    expect(pendingInvite()).toBe("first")
  })
})
