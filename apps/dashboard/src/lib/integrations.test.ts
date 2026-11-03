import { describe, expect, it } from "vitest"

import type {
  ConnectedRepository,
  Connection,
  GitHubInstallation,
  GitLabRepository,
} from "@/client/forge/loomplane/types"
import {
  cloudCatalogue,
  cloudHealth,
  fromGitHub,
  fromGitLab,
  groupRepositories,
  pickerGroupLabel,
  type PickerRepository,
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

const lab = (
  key: string,
  namespace: string,
  host = "gitlab.com",
  connection = "c1"
): GitLabRepository => ({
  key,
  fullName: key.slice(host.length + 1),
  name: key.split("/").pop() ?? key,
  namespace,
  host,
  connection,
  private: false,
  defaultBranch: "main",
})

describe("groupRepositories", () => {
  const installations = [installation("2", "xraph"), installation("1", "acme")]
  const label = pickerGroupLabel(installations)
  const repos = [
    repo("xraph/loomplane", "2"),
    repo("acme/web", "1"),
    repo("xraph/forge", "2"),
    repo("acme/api", "1"),
  ].map(fromGitHub)

  it("groups by account, each in name order", () => {
    expect(
      groupRepositories(repos, label).map((g) => [
        g.account,
        g.repositories.map((r) => r.fullName),
      ])
    ).toEqual([
      ["acme", ["acme/api", "acme/web"]],
      ["xraph", ["xraph/forge", "xraph/loomplane"]],
    ])
  })

  it("keeps the matches and drops a group left empty", () => {
    const groups = groupRepositories(repos, label, "  LOOM ")
    expect(groups).toHaveLength(1)
    expect(groups[0].account).toBe("xraph")
    expect(groups[0].avatarUrl).toBe("https://github.com/xraph.png")
    expect(groups[0].repositories.map((r) => r.fullName)).toEqual([
      "xraph/loomplane",
    ])
  })

  it("puts a repository whose installation isn't listed under its owner", () => {
    const groups = groupRepositories(
      [repo("gone/thing", "9"), repo("gone/other", "9")].map(fromGitHub),
      label
    )
    expect(groups.map((g) => [g.account, g.repositories.length])).toEqual([
      ["gone", 2],
    ])
  })

  it("shows nothing for an account with no repositories", () => {
    expect(groupRepositories([], label)).toEqual([])
  })

  it("groups GitLab projects by namespace, with the host when self-managed", () => {
    const gitlab = [
      lab("gitlab.com/grp/sub/api", "grp/sub"),
      lab("gitlab.com/grp/sub/web", "grp/sub"),
      lab("gitlab.com/solo/tool", "solo"),
      lab("git.acme.dev/team/api", "team", "git.acme.dev"),
      lab("gitlab.com/team/api", "team"),
    ].map(fromGitLab)
    expect(
      groupRepositories([...repos, ...gitlab], label).map((g) => [
        g.account,
        g.repositories.map((r) => r.key),
      ])
    ).toEqual([
      ["acme", ["acme/api", "acme/web"]],
      ["grp/sub", ["gitlab.com/grp/sub/api", "gitlab.com/grp/sub/web"]],
      ["solo", ["gitlab.com/solo/tool"]],
      ["team", ["gitlab.com/team/api"]],
      ["team on git.acme.dev", ["git.acme.dev/team/api"]],
      ["xraph", ["xraph/forge", "xraph/loomplane"]],
    ])
  })

  it("matches GitLab projects on their key as well as their name", () => {
    const gitlab = [
      lab("gitlab.com/grp/api", "grp"),
      lab("git.acme.dev/grp/api", "grp", "git.acme.dev"),
    ].map(fromGitLab)
    expect(
      groupRepositories(gitlab, label, "git.acme")
        .flatMap((g) => g.repositories)
        .map((r) => r.key)
    ).toEqual(["git.acme.dev/grp/api"])
  })
})

describe("fromGitHub and fromGitLab", () => {
  it("make a picker entry whose key is what a source stores", () => {
    expect(fromGitHub({ ...repo("acme/web", "1"), private: true })).toEqual({
      key: "acme/web",
      fullName: "acme/web",
      host: "github.com",
      provider: "github",
      private: true,
      group: "1",
      defaultBranch: "main",
    })
    expect(
      fromGitLab(lab("git.acme.dev/team/api", "team", "git.acme.dev"))
    ).toEqual({
      key: "git.acme.dev/team/api",
      fullName: "team/api",
      host: "git.acme.dev",
      provider: "gitlab",
      private: false,
      group: "gitlab:git.acme.dev:team",
      defaultBranch: "main",
    })
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
      {
        cloud: "gcp",
        title: "Google Cloud",
        builtin: true,
        methods: ["token"],
      },
      { cloud: "fly", title: "Fly.io", builtin: false, methods: ["token"] },
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
      "digitalocean",
    ])
  })
})

describe("repositoryChoices", () => {
  const installations = [installation("2", "xraph"), installation("1", "acme")]
  const label = pickerGroupLabel(installations)
  const repos: PickerRepository[] = [
    repo("xraph/loomplane", "2"),
    repo("xraph/forge", "2"),
    repo("acme/web", "1"),
  ].map(fromGitHub)
  const offered = (groups: ReturnType<typeof repositoryChoices>) =>
    groups.flatMap((g) =>
      g.items.map((c) => `${c.repository.key}${c.added ? " (added)" : ""}`)
    )

  it("offers what matches the typing, grouped by account", () => {
    const groups = repositoryChoices(repos, label, "xraph/")
    expect(groups.map((g) => g.account)).toEqual(["xraph"])
    expect(offered(groups)).toEqual(["xraph/forge", "xraph/loomplane"])
  })

  it("marks what the form already has, whatever its case", () => {
    expect(
      offered(repositoryChoices(repos, label, "", ["XRaph/Forge "]))
    ).toEqual(["acme/web", "xraph/forge (added)", "xraph/loomplane"])
  })

  it("marks an added GitLab project whatever its case", () => {
    const all = [...repos, fromGitLab(lab("gitlab.com/Grp/Api", "Grp"))]
    expect(
      offered(repositoryChoices(all, label, "", ["GITLAB.com/grp/api"]))
    ).toContain("gitlab.com/Grp/Api (added)")
  })

  it("offers everything again once the field holds one exactly", () => {
    expect(
      offered(repositoryChoices(repos, label, "xraph/loomplane"))
    ).toHaveLength(3)
    const all = [...repos, fromGitLab(lab("gitlab.com/grp/api", "grp"))]
    expect(
      offered(repositoryChoices(all, label, "gitlab.com/grp/api"))
    ).toHaveLength(4)
  })

  it("narrows GitLab projects as a host-qualified key is typed", () => {
    const all = [
      ...repos,
      fromGitLab(lab("gitlab.com/grp/api", "grp")),
      fromGitLab(lab("gitlab.com/grp/web", "grp")),
    ]
    expect(offered(repositoryChoices(all, label, "gitlab.com/grp/a"))).toEqual([
      "gitlab.com/grp/api",
    ])
  })

  it("offers nothing for a repository no account reaches", () => {
    expect(repositoryChoices(repos, label, "torvalds/linux")).toEqual([])
  })
})
