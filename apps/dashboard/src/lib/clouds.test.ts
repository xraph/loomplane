import { describe, expect, it } from "vitest"

import { cloudLabel, isKeyJSON, rolesFor } from "@/lib/clouds"

describe("rolesFor", () => {
  it("names each role once, in the order its permissions came", () => {
    expect(
      rolesFor([
        "run.services.create",
        "iam.serviceAccounts.actAs",
        "run.services.update",
        "storage.buckets.create",
      ]).map((r) => r.role)
    ).toEqual([
      "roles/run.admin",
      "roles/iam.serviceAccountUser",
      "roles/storage.admin",
    ])
  })

  it("skips a permission it has no role for", () => {
    expect(rolesFor(["compute.instances.create"])).toEqual([])
  })
})

describe("clouds", () => {
  it("labels known clouds and passes others through", () => {
    expect(cloudLabel("gcp")).toBe("Google Cloud")
    expect(cloudLabel("aws")).toBe("aws")
  })

  it("recognizes a service account key", () => {
    expect(isKeyJSON('{"type":"service_account"}')).toBe(true)
    expect(isKeyJSON('{"type":"authorized_user"}')).toBe(false)
    expect(isKeyJSON("not json")).toBe(false)
  })
})
