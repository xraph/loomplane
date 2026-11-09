import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { expect, it, vi } from "vitest"

import type { EntrySummary } from "@/client/forge/loomplane/types"
import { ModelCard } from "@/components/ai/models/model-card"

const entry: EntrySummary = {
  id: "meta-llama/Llama-3.1-8B-Instruct",
  title: "Llama 3.1 8B Instruct",
  publisher: "Meta",
  summary: "A small chat model with tool calls.",
  family: "llama",
  tasks: ["chat", "code"],
  parameters: 8030261248,
  contextLength: 131072,
  license: {
    id: "llama3.1",
    title: "Llama 3.1 Community License",
    url: "https://huggingface.co/meta-llama/Llama-3.1-8B-Instruct",
    commercial: true,
  },
  gated: true,
  verification: "estimated",
  testedOn: [],
  minGpuMemoryGib: 22,
  cpu: true,
}

it("never labels an estimate as tested", () => {
  render(<ModelCard entry={entry} onDeploy={() => {}} />)
  const card = screen.getByRole("article", { name: "Llama 3.1 8B Instruct" })
  expect(within(card).getByText("Estimated")).toBeInTheDocument()
  expect(card.textContent).not.toMatch(/tested/i)
  // Nor in what a screen reader hears.
  for (const el of card.querySelectorAll("[aria-label]"))
    expect(el.getAttribute("aria-label")).not.toMatch(/tested/i)
})

it("shows what a model is and needs, and deploys it", async () => {
  const onDeploy = vi.fn()
  render(<ModelCard entry={entry} onDeploy={onDeploy} />)
  const card = screen.getByRole("article")
  expect(within(card).getByText("Meta")).toBeInTheDocument()
  expect(within(card).getByText("Chat")).toBeInTheDocument()
  expect(within(card).getByText("Code")).toBeInTheDocument()
  expect(within(card).getByText("8B · 128K context")).toBeInTheDocument()
  expect(within(card).getByText("Gated")).toBeInTheDocument()
  expect(
    within(card).getByText(/Llama 3.1 Community License/)
  ).toBeInTheDocument()
  expect(
    within(card).getByText("Needs 22 GB GPU, or runs on CPU")
  ).toBeInTheDocument()
  await userEvent.click(within(card).getByRole("button", { name: "Deploy" }))
  expect(onDeploy).toHaveBeenCalledOnce()
})

it("turns Deploy off with the reason for someone who can't", () => {
  render(
    <ModelCard
      entry={{
        ...entry,
        gated: false,
        verification: "tested",
        testedOn: ["NVIDIA L40S"],
      }}
      deployBlocked="Ask a workspace admin for permission to deploy models."
    />
  )
  expect(screen.getByText("Tested on NVIDIA L40S")).toBeInTheDocument()
  expect(screen.queryByText("Gated")).toBeNull()
  const deploy = screen.getByRole("button", { name: "Deploy" })
  expect(deploy).toBeDisabled()
  expect(deploy).toHaveAttribute(
    "title",
    "Ask a workspace admin for permission to deploy models."
  )
})

it("keeps Deploy off until there's somewhere to deploy to", () => {
  render(<ModelCard entry={entry} />)
  expect(screen.getByRole("button", { name: "Deploy" })).toBeDisabled()
})
