import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { RepositoryPicker } from "@/components/repository-picker"

// What GitHub would say, without asking it: the connected accounts and the
// repositories they let Loomplane read.
const github = vi.hoisted(() => ({
  installations: [] as unknown[],
  repositories: [] as unknown[],
  repositoryError: false,
  refetch: vi.fn(),
}))
// And what a GitLab would say: its connected accounts and their projects.
const gitlab = vi.hoisted(() => ({
  connections: [] as unknown[],
  repositories: [] as unknown[],
}))

// And a Bitbucket's: whether it has a consumer, its workspaces and their
// repositories.
const bitbucket = vi.hoisted(() => ({
  configured: true,
  workspaces: [] as unknown[],
  repositories: [] as unknown[],
}))

vi.mock("@/client/hooks", async () => {
  const hooks = await import("@/client/forge/loomplane/hooks")
  return {
    useQuery: (op: unknown) => {
      if (op === hooks.useGithubStatus)
        return {
          status: "success",
          data: { configured: true, installations: github.installations },
        }
      if (op === hooks.useGitlabStatus)
        return {
          status: "success",
          data: {
            canConnect: true,
            hosts: [],
            connections: gitlab.connections,
          },
        }
      if (op === hooks.useBitbucketStatus)
        return {
          status: "success",
          data: {
            configured: bitbucket.configured,
            canConnect: true,
            problems: [],
            workspaces: bitbucket.workspaces,
          },
        }
      if (op === hooks.useBitbucketRepositories)
        return {
          status: "success",
          data: { repositories: bitbucket.repositories, problems: [] },
          refetch: vi.fn(),
        }
      if (op === hooks.useGitlabRepositories)
        return {
          status: "success",
          data: { repositories: gitlab.repositories },
          refetch: vi.fn(),
        }
      return {
        status: github.repositoryError ? "error" : "success",
        data: github.repositoryError
          ? undefined
          : { repositories: github.repositories },
        error: github.repositoryError
          ? new Error("GitHub is unavailable")
          : undefined,
        refetch: github.refetch,
      }
    },
  }
})

const account = (id: string, name: string) => ({
  id,
  account: name,
  accountType: "Organization",
  avatarUrl: "",
  manageUrl: "",
  createdAt: "2026-10-12T00:00:00Z",
})

const repository = (fullName: string, installation: string, priv = false) => ({
  fullName,
  installation,
  private: priv,
  defaultBranch: "main",
  url: `https://github.com/${fullName}`,
})

const project = (
  key: string,
  namespace: string,
  host = "gitlab.com",
  priv = false
) => ({
  key,
  fullName: key.slice(host.length + 1),
  name: key.split("/").pop(),
  namespace,
  host,
  connection: "c1",
  private: priv,
  defaultBranch: "main",
})

const workspace = (
  id: string,
  slug: string,
  name = slug,
  status = "ready"
) => ({
  id,
  workspace: slug,
  name,
  avatarUrl: "",
  manageUrl: "",
  connectedBy: "rex",
  status,
  message: "",
  createdAt: "2026-11-02T00:00:00Z",
})

const bbRepository = (fullName: string, connection: string, priv = false) => ({
  fullName,
  workspace: connection,
  private: priv,
  defaultBranch: "main",
})

function Field({
  exclude,
  initial = "",
}: {
  exclude?: string[]
  initial?: string
}) {
  const [value, setValue] = useState(initial)
  return (
    <>
      <RepositoryPicker
        aria-label="Repository"
        value={value}
        onChange={setValue}
        exclude={exclude}
      />
      <output aria-label="Chosen">{value}</output>
    </>
  )
}

beforeEach(() => {
  github.installations = []
  github.repositories = []
  github.repositoryError = false
  github.refetch.mockReset().mockResolvedValue(undefined)
  gitlab.connections = []
  gitlab.repositories = []
  bitbucket.configured = true
  bitbucket.workspaces = []
  bitbucket.repositories = []
})

describe("RepositoryPicker", () => {
  it("is a plain field until an account is connected", async () => {
    render(<Field />)
    const input = screen.getByRole("textbox", { name: "Repository" })
    await userEvent.type(input, "https://github.com/acme/web")
    expect(screen.getByLabelText("Chosen")).toHaveTextContent("acme/web")
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument()
  })

  it("takes a pasted GitLab URL as host/path", async () => {
    render(<Field />)
    await userEvent.type(
      screen.getByRole("textbox", { name: "Repository" }),
      "https://gitlab.com/grp/sub/api/-/tree/main"
    )
    expect(screen.getByLabelText("Chosen")).toHaveTextContent(
      "gitlab.com/grp/sub/api"
    )
  })

  it("takes a pasted Bitbucket URL as a lowercased host/path", async () => {
    render(<Field />)
    await userEvent.type(
      screen.getByRole("textbox", { name: "Repository" }),
      "https://rex@bitbucket.org/Acme/API/src/main/x.md"
    )
    expect(screen.getByLabelText("Chosen")).toHaveTextContent(
      "bitbucket.org/acme/api"
    )
    expect(screen.queryByText("github.com/")).not.toBeInTheDocument()
  })

  it("shows the github.com/ addon for an empty or bare value only", () => {
    const { unmount } = render(<Field />)
    expect(screen.getByText("github.com/")).toBeInTheDocument()
    unmount()
    const bare = render(<Field initial="acme/api" />)
    expect(screen.getByText("github.com/")).toBeInTheDocument()
    bare.unmount()
    render(<Field initial="gitlab.com/grp/api" />)
    expect(screen.queryByText("github.com/")).not.toBeInTheDocument()
  })

  it("hides the github.com/ addon for a host-qualified value with accounts connected", () => {
    github.installations = [account("1", "acme")]
    gitlab.connections = [{ id: "c1", host: "gitlab.com" }]
    render(<Field initial="gitlab.com/grp/api" />)
    expect(screen.queryByText("github.com/")).not.toBeInTheDocument()
  })

  describe("with only a GitLab connected", () => {
    beforeEach(() => {
      gitlab.connections = [{ id: "c1", host: "gitlab.com" }]
      gitlab.repositories = [
        project("gitlab.com/grp/sub/api", "grp/sub", "gitlab.com", true),
        project("gitlab.com/solo/tool", "solo"),
        project("git.acme.dev/team/web", "team", "git.acme.dev"),
      ]
    })

    it("lists its projects by namespace, with the host when self-managed", async () => {
      render(<Field />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const list = await screen.findByRole("listbox")
      expect(within(list).getAllByRole("group")).toHaveLength(3)
      expect(within(list).getByText("grp/sub")).toBeInTheDocument()
      expect(within(list).getByText("team on git.acme.dev")).toBeInTheDocument()
      expect(within(list).getAllByRole("option")).toHaveLength(3)
    })

    it("picks a project by its key", async () => {
      render(<Field />)
      const input = screen.getByRole("combobox", { name: "Repository" })
      await userEvent.type(input, "tool")
      await userEvent.keyboard("{ArrowDown}{Enter}")
      expect(screen.getByLabelText("Chosen")).toHaveTextContent(
        "gitlab.com/solo/tool"
      )
      expect(screen.queryByText("github.com/")).not.toBeInTheDocument()
    })

    it("won't pick a project the form already has, whatever its case", async () => {
      render(<Field exclude={["GitLab.com/solo/Tool"]} />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const added = await screen.findByRole("option", { name: /tool/ })
      expect(added).toHaveAttribute("aria-disabled", "true")
      expect(added).toHaveTextContent("Added")
    })
  })

  describe("with a Bitbucket workspace connected", () => {
    beforeEach(() => {
      bitbucket.workspaces = [workspace("w1", "acme", "Acme Inc")]
      bitbucket.repositories = [
        bbRepository("acme/api", "w1", true),
        bbRepository("acme/web", "w1"),
      ]
    })

    it("lists its repositories under the workspace, marked as Bitbucket", async () => {
      render(<Field />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const list = await screen.findByRole("listbox")
      const [group] = within(list).getAllByRole("group")
      expect(within(group).getByText("Acme Inc")).toBeInTheDocument()
      expect(within(group).getByLabelText("bitbucket.org")).toBeInTheDocument()
      expect(within(list).getAllByRole("option")).toHaveLength(2)
    })

    it("picks a repository by its bitbucket.org key", async () => {
      render(<Field />)
      await userEvent.type(
        screen.getByRole("combobox", { name: "Repository" }),
        "api"
      )
      await userEvent.keyboard("{ArrowDown}{Enter}")
      expect(screen.getByLabelText("Chosen")).toHaveTextContent(
        "bitbucket.org/acme/api"
      )
    })

    it("won't pick one the form already has, whatever its case", async () => {
      render(<Field exclude={["bitbucket.org/Acme/API"]} />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const added = await screen.findByRole("option", { name: /api/ })
      expect(added).toHaveAttribute("aria-disabled", "true")
      expect(added).toHaveTextContent("Added")
    })

    it("tells a GitHub acme from a Bitbucket acme by the host mark", async () => {
      github.installations = [account("1", "acme")]
      github.repositories = [repository("acme/site", "1")]
      render(<Field />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const list = await screen.findByRole("listbox")
      const groups = within(list).getAllByRole("group")
      expect(groups).toHaveLength(2)
      const hosts = groups.map((g) =>
        within(g).queryByLabelText("github.com")
          ? "github.com"
          : "bitbucket.org"
      )
      expect(hosts.sort()).toEqual(["bitbucket.org", "github.com"])
    })

    it("is a plain field when the consumer is not set up", () => {
      bitbucket.configured = false
      render(<Field />)
      expect(
        screen.getByRole("textbox", { name: "Repository" })
      ).toBeInTheDocument()
      expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    })

    it("is a plain field when no workspace is ready", () => {
      bitbucket.workspaces = [
        workspace("w1", "acme", "Acme Inc", "reconnect_needed"),
      ]
      render(<Field />)
      expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    })
  })

  describe("with connected accounts", () => {
    beforeEach(() => {
      github.installations = [account("2", "xraph"), account("1", "acme")]
      github.repositories = [
        repository("xraph/loomplane", "2", true),
        repository("xraph/forge", "2"),
        repository("acme/web", "1"),
      ]
    })

    it("lists them grouped by account when the field is clicked", async () => {
      render(<Field />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const list = await screen.findByRole("listbox")
      expect(within(list).getAllByRole("group")).toHaveLength(2)
      expect(within(list).getByText("acme")).toBeInTheDocument()
      expect(within(list).getByText("xraph")).toBeInTheDocument()
      expect(within(list).getAllByRole("option")).toHaveLength(3)
    })

    it("narrows as you type, and Enter picks the highlighted one", async () => {
      render(<Field />)
      const input = screen.getByRole("combobox", { name: "Repository" })
      await userEvent.type(input, "loom")
      const list = await screen.findByRole("listbox")
      expect(within(list).getAllByRole("option")).toHaveLength(1)
      await userEvent.keyboard("{ArrowDown}{Enter}")
      expect(screen.getByLabelText("Chosen")).toHaveTextContent(
        "xraph/loomplane"
      )
    })

    it("won't pick one the form already has", async () => {
      render(<Field exclude={["acme/web"]} />)
      await userEvent.click(
        screen.getByRole("combobox", { name: "Repository" })
      )
      const added = await screen.findByRole("option", { name: /web/ })
      expect(added).toHaveAttribute("aria-disabled", "true")
      expect(added).toHaveTextContent("Added")
      await userEvent.click(added)
      expect(screen.getByLabelText("Chosen")).toHaveTextContent("")
    })

    it("shows a GitHub failure and lets you retry without losing what's typed", async () => {
      github.repositoryError = true
      render(<Field />)
      const input = screen.getByRole("combobox", { name: "Repository" })
      await userEvent.type(input, "acme/private")
      expect(screen.getByRole("alert")).toHaveTextContent(
        "GitHub is unavailable"
      )
      await userEvent.click(
        screen.getByRole("button", { name: "Retry repositories" })
      )
      expect(github.refetch).toHaveBeenCalledOnce()
      expect(screen.getByLabelText("Chosen")).toHaveTextContent("acme/private")
    })

    it("still takes any public repository typed in full", async () => {
      render(<Field />)
      await userEvent.type(
        screen.getByRole("combobox", { name: "Repository" }),
        "torvalds/linux"
      )
      expect(screen.getByLabelText("Chosen")).toHaveTextContent(
        "torvalds/linux"
      )
    })
  })
})
