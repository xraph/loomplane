import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { HFInspect } from "@/components/ai/models/hf-inspect"

const access = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    can: (permission: string) =>
      permission === "ai:manage" ? access.manage : true,
  }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.manage = true
})

type Sent = { path: string; body: unknown }

function stub(reply: (body: { repo: string }) => Response): Sent[] {
  const sent: Sent[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      sent.push({ path, body })
      if (path === "/api/v1/ai/models/inspect") return reply(body)
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  return sent
}

const pasted = {
  id: "hf:acme/tiny-chat@0123456789ab",
  title: "tiny-chat",
  publisher: "acme",
  summary: "",
  family: "",
  tasks: ["chat"],
  parameters: 494032768,
  context_length: 32768,
  license: {
    id: "unknown",
    title: "unknown",
    url: "https://huggingface.co/acme/tiny-chat",
    commercial: false,
    note: "Read the license on Hugging Face before you deploy.",
  },
  gated: false,
  verification: "untested",
  tested_on: [],
  min_gpu_memory_gib: 4,
  cpu: true,
  fits_notice: "Couldn't check your GPUs.",
  revision: "0123456789abcdef0123456789abcdef01234567",
  hugging_face_url: "https://huggingface.co/acme/tiny-chat",
  quantizations: [
    {
      id: "gguf-q8_0",
      engine: "llamacpp",
      weight_bytes: 9_000_000_000,
      fits: [
        {
          gpu_kind: "cpu",
          need: { gpus: 0, max_model_len: 32768, per_gpu_gib: 0 },
          status: "no-fit",
          reason:
            "Its weights are larger than the 8 GB cache a CPU model gets on Kubernetes.",
        },
      ],
    },
  ],
}

function Wrapped({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

it("inspects a pasted repo and offers an untested deploy", async () => {
  const sent = stub(() => Response.json(pasted))
  const onDeploy = vi.fn()
  render(
    <Wrapped>
      <HFInspect onDeploy={onDeploy} />
    </Wrapped>
  )
  const section = screen.getByRole("region", { name: "Any Hugging Face model" })
  await userEvent.type(
    within(section).getByPlaceholderText(
      "Paste a Hugging Face repo, like Qwen/Qwen2.5-7B-Instruct"
    ),
    "https://huggingface.co/acme/tiny-chat"
  )
  await userEvent.click(within(section).getByRole("button", { name: "Check" }))

  const card = await within(section).findByRole("article", {
    name: "tiny-chat",
  })
  expect(sent).toEqual([
    { path: "/api/v1/ai/models/inspect", body: { repo: "acme/tiny-chat" } },
  ])
  expect(within(card).getByText("Untested")).toBeInTheDocument()
  expect(
    within(section).getByText("Couldn't check your GPUs.")
  ).toBeInTheDocument()
  expect(
    within(section).getByText(
      "Its weights are larger than the 8 GB cache a CPU model gets on Kubernetes."
    )
  ).toBeInTheDocument()
  await userEvent.click(within(card).getByRole("button", { name: "Deploy" }))
  expect(onDeploy).toHaveBeenCalledWith(
    expect.objectContaining({ id: "hf:acme/tiny-chat@0123456789ab" })
  )
})

it("shows the server's reason for a repo it can't serve", async () => {
  const gated =
    "meta-llama/Llama-3.1-8B-Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings"
  const remote =
    "acme/odd-model asks to run its repository's code, which Loomplane never does."
  stub(({ repo }) =>
    Response.json(
      {
        code: repo.startsWith("acme") ? "invalid" : "gated",
        message: repo.startsWith("acme") ? remote : gated,
      },
      { status: 422 }
    )
  )
  const { unmount } = render(
    <Wrapped>
      <HFInspect onDeploy={() => {}} />
    </Wrapped>
  )
  const field = screen.getByRole("textbox", { name: "Hugging Face repo" })
  await userEvent.type(field, "meta-llama/Llama-3.1-8B-Instruct")
  await userEvent.click(screen.getByRole("button", { name: "Check" }))
  const alert = await screen.findByRole("alert")
  expect(alert).toHaveTextContent(`${gated}.`)
  expect(
    within(alert).getByRole("link", { name: "Open AI settings" })
  ).toHaveAttribute("href", "/ai/settings")
  expect(screen.queryByRole("article")).toBeNull()

  await userEvent.clear(field)
  await userEvent.type(field, "acme/odd-model")
  await userEvent.click(screen.getByRole("button", { name: "Check" }))
  expect(await screen.findByText(remote)).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Open AI settings" })).toBeNull()
  unmount()

  // A member can't change AI settings, so the refusal says who can.
  access.manage = false
  render(
    <Wrapped>
      <HFInspect onDeploy={() => {}} />
    </Wrapped>
  )
  await userEvent.type(
    screen.getByRole("textbox", { name: "Hugging Face repo" }),
    "meta-llama/Llama-3.1-8B-Instruct"
  )
  await userEvent.click(screen.getByRole("button", { name: "Check" }))
  const refusal = await screen.findByRole("alert")
  expect(refusal).toHaveTextContent(
    "Ask a workspace admin to change it in AI settings."
  )
  expect(within(refusal).queryByRole("link")).toBeNull()
})

it("never sends a pasted token", async () => {
  const sent = stub(() => Response.json(pasted))
  render(
    <Wrapped>
      <HFInspect onDeploy={() => {}} />
    </Wrapped>
  )
  const field = screen.getByRole("textbox", { name: "Hugging Face repo" })
  await userEvent.type(field, "hf_abcdefghijklmnopqrstuvwxyz")
  await userEvent.click(screen.getByRole("button", { name: "Check" }))
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "That looks like a Hugging Face token."
  )
  expect(sent).toEqual([])
  expect(field).toHaveValue("")
  expect(document.body.textContent).not.toContain("hf_abcdefghijklmnop")
})
