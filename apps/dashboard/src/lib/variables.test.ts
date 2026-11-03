import { describe, expect, it } from "vitest"

import type { Variable } from "@/client/forge/loomplane/types"
import {
  keyValid,
  kindFor,
  looksSecret,
  layerLabel,
  ownLayer,
  reachLabel,
} from "@/lib/variables"

const variable = (over: Partial<Variable>): Variable => ({
  id: "var_1",
  orgId: "aorg_1",
  scope: "project",
  key: "PORT",
  value: "3000",
  secret: false,
  environment: "",
  createdAt: "2026-10-12T00:00:00Z",
  createdBy: "ausr_1",
  updatedAt: "2026-10-12T00:00:00Z",
  updatedBy: "ausr_1",
  ...over,
})

describe("ownLayer", () => {
  const web = "svc:node:apps/web"
  it.each([
    ["a project variable, looking at the project", variable({}), "", true],
    ["a project variable, looking at a service", variable({}), web, false],
    [
      "the service's variable",
      variable({ scope: "service", service: web }),
      web,
      true,
    ],
    [
      "another service's variable",
      variable({ scope: "service", service: "svc:go:api" }),
      web,
      false,
    ],
    [
      "an organization variable",
      variable({ scope: "organization" }),
      "",
      false,
    ],
  ])("%s", (_, v, service, want) => {
    expect(ownLayer(v, { service })).toBe(want)
  })
})

describe("labels", () => {
  it("names the layer and where it applies", () => {
    expect(layerLabel(variable({ scope: "organization" }))).toBe("Workspace")
    expect(reachLabel(variable({}))).toBe("every environment")
    expect(reachLabel(variable({ environment: "production" }))).toBe(
      "production only"
    )
  })
})

describe("kindFor", () => {
  it("matches the API's guess from a name", () => {
    expect(kindFor("staging")).toBe("staging")
    expect(kindFor("qa-eu")).toBe("development")
  })
})

describe("keyValid", () => {
  it.each([
    ["DATABASE_URL", true],
    ["_private", true],
    ["1BAD", false],
    ["MY-KEY", false],
    ["", false],
  ])("%s is %s", (key, want) => {
    expect(keyValid(key)).toBe(want)
  })
})

describe("looksSecret", () => {
  it.each([
    ["SESSION_SECRET", "", true],
    ["GITHUB_TOKEN", "", true],
    ["DB_PASSWORD", "", true],
    ["STRIPE_API_KEY", "", true],
    ["APP_KEY", "", true],
    ["AWS_SECRET_ACCESS_KEY", "", true],
    ["JWT_SIGNING_KEY", "", true],
    ["DATABASE_URL", "", true],
    ["DIRECT_DATABASE_URL", "", true],
    ["SENTRY_DSN", "", true],
    ["LOG_LEVEL", "info", false],
    ["API_URL", "https://api.example.test", false],
    ["AWS_ACCESS_KEY_ID", "AKIAABCDEFGHIJKLMNOP", false],
    ["JWT_PUBLIC_KEY", "", false],
    ["STRIPE_PUBLISHABLE_KEY", "pk_live_x", false],
    ["TLS_KEY_PATH", "/etc/tls/key.pem", false],
    ["TOKEN_TTL", "3600", false],
    ["OAUTH_TOKEN_URL", "https://auth.example.test/token", false],
    ["REDIRECT_URI", "https://app.example.test/cb", false],
    ["NEXT_PUBLIC_API_KEY", "", false],
    ["VITE_SECRET", "", false],
  ])("%s=%s is %s by its key", (key, value, want) => {
    expect(looksSecret(key, value)).toBe(want)
  })

  it.each([
    ["STRIPE", "sk_live_abc123"],
    ["GH", "ghp_abcdefghijklmnopqrstuvwxyz0123"],
    ["SLACK", "xoxb-1-2-abc"],
    ["OPENAI", "sk-proj-abcdefghijklmnopqrstuvwx"],
    ["CERT", "-----BEGIN RSA PRIVATE KEY-----\nabc"],
    ["CACHE_URL", "redis://:hunter2@cache:6379"],
    ["UPSTREAM", "postgres://app:hunter2@db/app"],
  ])("%s is a secret by its value", (key, value) => {
    expect(looksSecret(key, value)).toBe(true)
  })

  it("leaves a URL without a password plain", () => {
    expect(looksSecret("UPSTREAM", "https://user@example.test/x")).toBe(false)
  })
})
