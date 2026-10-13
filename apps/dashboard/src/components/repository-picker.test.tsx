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

vi.mock("@/client/hooks", async () => {
  const hooks = await import("@/client/forge/loomplane/hooks")
  return {
    useQuery: (op: unknown) =>
      op === hooks.useGithubStatus
        ? {
            status: "success",
            data: { configured: true, installations: github.installations },
          }
        : {
            status: github.repositoryError ? "error" : "success",
            data: github.repositoryError
              ? undefined
              : { repositories: github.repositories },
            error: github.repositoryError
              ? new Error("GitHub is unavailable")
              : undefined,
            refetch: github.refetch,
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

function Field({ exclude }: { exclude?: string[] }) {
  const [value, setValue] = useState("")
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
})

describe("RepositoryPicker", () => {
  it("is a plain field until an account is connected", async () => {
    render(<Field />)
    const input = screen.getByRole("textbox", { name: "Repository" })
    await userEvent.type(input, "https://github.com/acme/web")
    expect(screen.getByLabelText("Chosen")).toHaveTextContent("acme/web")
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument()
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
