import { cleanup, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setFeatures } from "@/lib/features"
import {
  detail,
  dialogRoutes,
  open,
  qwen,
  stub,
} from "@/test-fixtures/ai-models"

const perms = vi.hoisted(() => ({ edit: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    can: (permission: string) => (permission === "ai:read" ? true : perms.edit),
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => null }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  setFeatures({ ai: true })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  setFeatures({ ai: false })
  perms.edit = true
})

it("redirects a model's page to / when AI is off", async () => {
  setFeatures({ ai: false })
  stub(dialogRoutes())
  const router = open(`/ai/models/${qwen}`)
  await waitFor(() => expect(router.state.location.pathname).toBe("/"))
})

it("shows the model, its license and its quantizations", async () => {
  const asked = stub(dialogRoutes())
  open(`/ai/models/${qwen}`)
  expect(
    await screen.findByRole("heading", {
      name: "Qwen2.5 7B Instruct",
      level: 1,
    })
  ).toBeInTheDocument()
  // The splat is the repo, slash and all.
  expect(
    asked
      .find((a) => a.url.pathname === "/api/v1/ai/models/registry/entry")
      ?.url.searchParams.get("id")
  ).toBe(qwen)

  const license = screen.getByRole("region", { name: "License" })
  expect(license).toHaveTextContent("Apache 2.0")
  expect(license).toHaveTextContent("Commercial use allowed")
  expect(screen.getByRole("link", { name: /Hugging Face/ })).toHaveAttribute(
    "href",
    `https://huggingface.co/${qwen}`
  )

  const rows = screen
    .getAllByRole("row")
    .filter((r) => /bf16|gguf-q4_k_m/.test(r.textContent ?? ""))
  const [bf16, gguf] = rows
  expect(bf16).toHaveTextContent("vLLM")
  expect(bf16).toHaveTextContent("15.2 GB")
  expect(bf16).toHaveTextContent("18 GB")
  expect(within(bf16).getByRole("link", { name: "aaaaaaa" })).toHaveAttribute(
    "href",
    `https://huggingface.co/${qwen}/commit/${"a".repeat(40)}`
  )
  // A GGUF lives in its own repo, at its own commit, and its GPU memory
  // comes from its fits.
  expect(gguf).toHaveTextContent("llama.cpp")
  expect(gguf).toHaveTextContent("6 GB, or CPU")
  expect(within(gguf).getByRole("link", { name: "bbbbbbb" })).toHaveAttribute(
    "href",
    `https://huggingface.co/Qwen/Qwen2.5-7B-Instruct-GGUF/commit/${"b".repeat(40)}`
  )
})

it("shows tested, estimated and failed per GPU kind", async () => {
  stub(dialogRoutes())
  open(`/ai/models/${qwen}`)
  const table = await screen.findByRole("table", { name: "Where bf16 fits" })
  const row = (gpu: string) =>
    within(table)
      .getAllByRole("row")
      .find((r) => (r as HTMLTableRowElement).cells[0]?.textContent === gpu)!

  const l40s = row("NVIDIA L40S")
  expect(l40s).toHaveTextContent("32,768 tokens")
  expect(
    within(l40s).getByRole("button", { name: /^Tested on NVIDIA L40S/ })
  ).toBeInTheDocument()

  const l4 = row("NVIDIA L4")
  expect(l4).toHaveTextContent("8,192 tokens")
  // An estimate never reads as tested.
  const estimated = within(l4).getByRole("button", { name: /^Estimated/ })
  expect(estimated.getAttribute("aria-label")).not.toMatch(/tested/i)

  const t4 = row("NVIDIA T4")
  expect(
    within(t4).getByRole("button", { name: /^Doesn't fit NVIDIA T4/ })
  ).toBeInTheDocument()
  expect(t4).toHaveTextContent("It needs 18 GB on one GPU, and a T4 has 16 GB.")

  const a10 = row("NVIDIA A10")
  expect(
    within(a10).getByRole("button", { name: /^Failed on NVIDIA A10/ })
  ).toBeInTheDocument()
  expect(a10).toHaveTextContent("CUDA out of memory at 32768 tokens.")
  // A failed or no-fit row offers no GPU count or context.
  expect(a10).not.toHaveTextContent("tokens fit")

  // The other quantization's tab shows its own fits, CPU among them.
  await userEvent.click(screen.getByRole("tab", { name: "gguf-q4_k_m" }))
  const gguf = await screen.findByRole("table", {
    name: "Where gguf-q4_k_m fits",
  })
  expect(gguf).toHaveTextContent("CPU")
})

it("renders the model card from Hugging Face", async () => {
  stub({
    ...dialogRoutes(),
    "/api/v1/ai/models/registry/card": () =>
      Response.json({
        markdown: "## Quickstart\n\n<script>alert(1)</script>\n\nServe it.",
        source_url: `https://huggingface.co/${qwen}`,
      }),
  })
  open(`/ai/models/${qwen}`)
  expect(
    await screen.findByRole("heading", { name: "Quickstart" })
  ).toBeInTheDocument()
  expect(document.querySelector("script")).toBeNull()
  expect(
    screen.getByRole("link", { name: /Read it on Hugging Face/ })
  ).toHaveAttribute("href", `https://huggingface.co/${qwen}`)
})

it("opens the deploy dialog, and keeps it from members", async () => {
  stub(dialogRoutes())
  open(`/ai/models/${qwen}`)
  await userEvent.click(await screen.findByRole("button", { name: "Deploy" }))
  expect(
    await screen.findByRole("dialog", { name: "Deploy Qwen2.5 7B Instruct" })
  ).toBeInTheDocument()
  cleanup()
  forgeClient.clear()

  perms.edit = false
  stub(dialogRoutes())
  open(`/ai/models/${qwen}`)
  const deploy = await screen.findByRole("button", { name: "Deploy" })
  expect(deploy).toBeDisabled()
  expect(deploy).toHaveAttribute(
    "title",
    "Ask a workspace admin for permission to deploy models."
  )
})

it("renders loading, denied, missing and failed distinctly", async () => {
  let reply: () => Response | Promise<Response> = () =>
    new Promise<Response>(() => {})
  stub({
    ...dialogRoutes(),
    "/api/v1/ai/models/registry/entry": () => reply(),
  })
  open(`/ai/models/${qwen}`)
  expect(await screen.findByTestId("entry-loading")).toBeInTheDocument()
  cleanup()
  forgeClient.clear()

  reply = () =>
    Response.json({ code: "forbidden", message: "forbidden" }, { status: 403 })
  open(`/ai/models/${qwen}`)
  expect(await screen.findByText("Models are restricted")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  cleanup()
  forgeClient.clear()

  reply = () =>
    Response.json(
      { code: "not_found", message: "the model registry has no x/y" },
      { status: 404 }
    )
  open("/ai/models/x/y")
  expect(await screen.findByText("No such model")).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Browse the registry" })
  ).toHaveAttribute("href", "/ai/models")
  cleanup()
  forgeClient.clear()

  reply = () =>
    Response.json({ code: "internal", message: "boom" }, { status: 500 })
  open(`/ai/models/${qwen}`)
  expect(
    await screen.findByRole("button", { name: "Retry" })
  ).toBeInTheDocument()
})

it("keeps the entry free of raw HTML even in its summary", async () => {
  stub({
    ...dialogRoutes(),
    "/api/v1/ai/models/registry/entry": () =>
      Response.json(detail({ summary: "<b>bold</b> claims" })),
  })
  open(`/ai/models/${qwen}`)
  expect(await screen.findByText("<b>bold</b> claims")).toBeInTheDocument()
})
