import { describe, expect, test } from "vitest"

import {
  hostLabel,
  repositoryHost,
  repositoryKey,
  repositoryURL,
  sourceURL,
  startsWithHost,
} from "@/lib/format"

describe("repositoryKey", () => {
  test("reads GitLab URLs", () => {
    expect(repositoryKey("https://gitlab.com/grp/sub/api/-/tree/main")).toBe(
      "gitlab.com/grp/sub/api"
    )
    expect(repositoryKey("git@GitLab.example.com:team/api.git")).toBe(
      "gitlab.example.com/team/api"
    )
    expect(repositoryKey("https://github.com/acme/api.git")).toBe("acme/api")
    expect(repositoryKey("acme/api")).toBe("acme/api")
  })

  test("keeps the path's case for GitLab and GitHub, and lowercases the host", () => {
    expect(repositoryKey("https://GitLab.com/Grp/Api")).toBe(
      "gitlab.com/Grp/Api"
    )
    expect(repositoryKey("https://GitHub.com/Acme/Api")).toBe("Acme/Api")
  })

  test("reads Bitbucket page URLs down to workspace and repository", () => {
    for (const url of [
      "https://bitbucket.org/acme/api",
      "https://bitbucket.org/acme/api/src/main/README.md",
      "https://bitbucket.org/acme/api/commits/abc123",
      "https://bitbucket.org/acme/api/branch/x",
      "https://bitbucket.org/acme/api/pull-requests/1",
      "https://bitbucket.org/acme/api.git",
    ]) {
      expect(repositoryKey(url)).toBe("bitbucket.org/acme/api")
    }
    expect(
      repositoryKey("https://rex@bitbucket.org/Acme/API/src/main/x.md")
    ).toBe("bitbucket.org/acme/api")
    expect(repositoryKey("git@bitbucket.org:acme/api.git")).toBe(
      "bitbucket.org/acme/api"
    )
  })

  test("reads GitHub page URLs down to owner and name", () => {
    expect(repositoryKey("https://github.com/acme/api/tree/main/x")).toBe(
      "acme/api"
    )
  })

  test("drops userinfo, a trailing slash and .git for every host", () => {
    expect(repositoryKey("https://oauth2:tok@gitlab.com/grp/api.git/")).toBe(
      "gitlab.com/grp/api"
    )
    expect(repositoryKey("  https://gitlab.com/grp/api/  ")).toBe(
      "gitlab.com/grp/api"
    )
  })

  test("takes a host-qualified key as it is", () => {
    expect(repositoryKey("gitlab.com/grp/sub/api")).toBe(
      "gitlab.com/grp/sub/api"
    )
    expect(repositoryKey("github.com/acme/api")).toBe("acme/api")
  })

  test("keeps a self-managed host's port", () => {
    expect(repositoryKey("https://git.example.com:8443/grp/api")).toBe(
      "git.example.com:8443/grp/api"
    )
  })

  test("is empty for what isn't a repository", () => {
    for (const v of [
      "",
      "  ",
      "acme",
      "https://gitlab.com/grp",
      "https://github.com/acme",
      "https://gitlab.com/-/tree/main",
      "a/b/c",
    ]) {
      expect(repositoryKey(v)).toBe("")
    }
  })
})

describe("repositoryURL", () => {
  test("turns any key into a web URL", () => {
    expect(repositoryURL("acme/api")).toBe("https://github.com/acme/api")
    expect(repositoryURL("gitlab.com/grp/sub/api")).toBe(
      "https://gitlab.com/grp/sub/api"
    )
    expect(repositoryURL("https://gitlab.com/grp/api")).toBe(
      "https://gitlab.com/grp/api"
    )
  })

  test("takes a host with only a port", () => {
    expect(repositoryURL("localhost:8080/grp/api")).toBe(
      "https://localhost:8080/grp/api"
    )
    expect(repositoryKey("localhost:8080/grp/api")).toBe(
      "localhost:8080/grp/api"
    )
    expect(startsWithHost("localhost:8080/grp")).toBe(true)
    expect(startsWithHost("acme/api")).toBe(false)
  })

  test("leaves a git@ address and a half-typed value as they are", () => {
    expect(repositoryURL("git@gitlab.com:grp/api.git")).toBe(
      "git@gitlab.com:grp/api.git"
    )
    expect(repositoryURL("acme")).toBe("acme")
  })
})

describe("repositoryHost", () => {
  test("is github.com for a bare key, else the first segment", () => {
    expect(repositoryHost("acme/api")).toBe("github.com")
    expect(repositoryHost("gitlab.com/grp/sub/api")).toBe("gitlab.com")
    expect(repositoryHost("git.example.com:8443/grp/api")).toBe(
      "git.example.com:8443"
    )
    expect(repositoryHost("localhost:8080/grp/api")).toBe("localhost:8080")
    expect(repositoryHost("")).toBe("github.com")
  })
})

describe("startsWithHost", () => {
  test("is true once the first segment has a dot or a port", () => {
    expect(startsWithHost("gitlab.com/grp")).toBe(true)
    expect(startsWithHost("  git.acme.dev")).toBe(true)
    expect(startsWithHost("localhost:8080/grp/api")).toBe(true)
    expect(startsWithHost("acme/api")).toBe(false)
    expect(startsWithHost("acme")).toBe(false)
    expect(startsWithHost("")).toBe(false)
    // A dot later on is a repository name, not a host.
    expect(startsWithHost("acme/api.js")).toBe(false)
  })
})

describe("sourceURL", () => {
  test("links to the repository when there's no directory", () => {
    expect(sourceURL({ repository: "acme/api", root: "", ref: "main" })).toBe(
      "https://github.com/acme/api"
    )
    expect(sourceURL({ repository: "gitlab.com/grp/sub/api" })).toBe(
      "https://gitlab.com/grp/sub/api"
    )
  })

  test("uses each host's own page for a directory", () => {
    expect(
      sourceURL({ repository: "acme/api", root: "cmd/api", ref: "main" })
    ).toBe("https://github.com/acme/api/tree/main/cmd/api")
    expect(
      sourceURL({
        repository: "bitbucket.org/acme/api",
        root: "cmd/api",
        ref: "dev",
      })
    ).toBe("https://bitbucket.org/acme/api/src/dev/cmd/api")
    expect(
      sourceURL({
        repository: "gitlab.com/grp/sub/api",
        root: "cmd/api",
        ref: "main",
      })
    ).toBe("https://gitlab.com/grp/sub/api/-/tree/main/cmd/api")
    // Any other host is taken for a self-managed GitLab.
    expect(
      sourceURL({ repository: "git.acme.dev/team/api", root: "svc" })
    ).toBe("https://git.acme.dev/team/api/-/tree/HEAD/svc")
  })
})

describe("hostLabel", () => {
  test("names GitHub, and any other host by itself", () => {
    expect(hostLabel("acme/api")).toBe("GitHub")
    expect(hostLabel("gitlab.com/grp/api")).toBe("gitlab.com")
    expect(hostLabel("bitbucket.org/acme/api")).toBe("bitbucket.org")
    expect(hostLabel("git.acme.dev:8443/team/api")).toBe("git.acme.dev:8443")
  })
})
