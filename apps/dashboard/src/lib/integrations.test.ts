import { describe, expect, it } from "vitest"

import type {
  ConnectedRepository,
  Connection,
  GitHubInstallation,
} from "@/client/forge/loomplane/types"
import {
  cloudCatalogue,
  cloudHealth,
  groupRepositories,
  repositoryChoices,
} from "@/lib/integrations"

const installation = (id: string, account: string): GitHubInstallation => ({
  id,
  account,
  accountType: "Organization",
  avatarUrl: `https://github.com/${account}.png`,
  manageUrl: `https://github.com/organizations/${account}/settings/installations/${id}`,
  createdAt: "2026-10-12T00:00:00Z",
})

const repo = (fullName: string, installation: string): ConnectedRepository => ({
  fullName,
  installation,
  private: false,
  defaultBranch: "main",
  url: `https://github.com/${fullName}`,
})

describe("groupRepositories", () => {
  const installations = [installation("2", "xraph"), installation("1", "acme")]
  const repos = [
    repo("xraph/loomplane", "2"),
    repo("acme/web", "1"),
    repo("xraph/forge", "2"),
    repo("acme/api", "1"),
  ]

  it("groups by account, each in name order", () => {
    expect(
      groupRepositories(repos, installations).map((g) => [
        g.account,
        g.repositories.map((r) => r.fullName),
      ])
    ).toEqual([
      ["acme", ["acme/api", "acme/web"]],
      ["xraph", ["xraph/forge", "xraph/loomplane"]],
    ])
  })

  it("keeps the matches and drops a group left empty", () => {
    const groups = groupRepositories(repos, installations, "  LOOM ")
    expect(groups).toHaveLength(1)
    expect(groups[0].account).toBe("xraph")
    expect(groups[0].avatarUrl).toBe("https://github.com/xraph.png")
    expect(groups[0].repositories.map((r) => r.fullName)).toEqual([
      "xraph/loomplane",
    ])
  })

  it("puts a repository whose installation isn't listed under its owner", () => {
    const groups = groupRepositories(
      [repo("gone/thing", "9"), repo("gone/other", "9")],
      installations
    )
    expect(groups.map((g) => [g.key, g.repositories.length])).toEqual([
      ["gone", 2],
    ])
  })

  it("shows nothing for an account with no repositories", () => {
    expect(groupRepositories([], installations)).toEqual([])
  })
})

describe("cloudHealth", () => {
  const conn = (status: string) => ({ status }) as Connection

  it("counts what's ready and what needs attention", () => {
    expect(
      cloudHealth([conn("ready"), conn("incomplete"), conn("failed")])
    ).toEqual({ ready: 1, attention: 2, total: 3 })
    expect(cloudHealth([])).toEqual({ ready: 0, attention: 0, total: 0 })
  })
})

describe("cloudCatalogue", () => {
  const conn = (cloud: string) => ({ cloud, status: "ready" }) as Connection

  it("follows the API's list, then adds a cloud only a connection is on", () => {
    const forms = [
      { cloud: "gcp", title: "Google Cloud", builtin: true },
      { cloud: "fly", title: "Fly.io", builtin: false },
    ]
    expect(
      cloudCatalogue(forms, [conn("fly"), conn("aws")]).map((t) => t.title)
    ).toEqual(["Google Cloud", "Fly.io", "AWS"])
  })

  it("shows the built-in clouds before the API answers", () => {
    expect(cloudCatalogue(undefined, []).map((t) => t.cloud)).toEqual([
      "gcp",
      "aws",
      "vercel",
      "render",
    ])
  })
})

describe("repositoryChoices", () => {
  const installations = [installation("2", "xraph"), installation("1", "acme")]
  const repos = [
    repo("xraph/loomplane", "2"),
    repo("xraph/forge", "2"),
    repo("acme/web", "1"),
  ]
  const offered = (groups: ReturnType<typeof repositoryChoices>) =>
    groups.flatMap((g) =>
      g.items.map((c) => `${c.repository.fullName}${c.added ? " (added)" : ""}`)
    )

  it("offers what matches the typing, grouped by account", () => {
    const groups = repositoryChoices(repos, installations, "xraph/")
    expect(groups.map((g) => g.account)).toEqual(["xraph"])
    expect(offered(groups)).toEqual(["xraph/forge", "xraph/loomplane"])
  })

  it("marks what the form already has, whatever its case", () => {
    expect(
      offered(repositoryChoices(repos, installations, "", ["XRaph/Forge "]))
    ).toEqual(["acme/web", "xraph/forge (added)", "xraph/loomplane"])
  })

  it("offers everything again once the field holds one exactly", () => {
    expect(
      offered(repositoryChoices(repos, installations, "xraph/loomplane"))
    ).toHaveLength(3)
  })

  it("offers nothing for a repository no account reaches", () => {
    expect(repositoryChoices(repos, installations, "torvalds/linux")).toEqual(
      []
    )
  })
})
