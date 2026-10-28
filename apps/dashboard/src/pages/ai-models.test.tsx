import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setFeatures } from "@/lib/features"
import { createDashboardRouter } from "@/router"
import { dialogRoutes } from "@/test-fixtures/ai-models"

const access = vi.hoisted(() => ({ edit: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    can: (permission: string) =>
      permission === "ai:edit" || permission === "ai:manage"
        ? access.edit
        : true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
// Only the routes and their pages, not the shell.
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => null }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))

function entry(over: Record<string, unknown> = {}) {
  return {
    id: "Qwen/Qwen2.5-7B-Instruct",
    title: "Qwen2.5 7B Instruct",
    publisher: "Qwen",
    summary: "A chat model that also writes code.",
    family: "qwen",
    tasks: ["chat", "code"],
    parameters: 7615616512,
    context_length: 131072,
    license: {
      id: "apache-2.0",
      title: "Apache 2.0",
      url: "https://www.apache.org/licenses/LICENSE-2.0",
      commercial: true,
    },
    gated: false,
    verification: "tested",
    tested_on: ["NVIDIA L40S"],
    min_gpu_memory_gib: 18,
    cpu: true,
    ...over,
  }
}

const registry = {
  entries: [
    entry(),
    entry({
      id: "meta-llama/Llama-3.1-8B-Instruct",
      title: "Llama 3.1 8B Instruct",
      publisher: "Meta",
      gated: true,
      verification: "estimated",
      tested_on: [],
      license: {
        id: "llama3.1",
        title: "Llama 3.1 Community License",
        url: "https://huggingface.co/meta-llama/Llama-3.1-8B-Instruct",
        commercial: true,
      },
    }),
  ],
  counts: { chat: 2, code: 2 },
}

type Handler = (url: URL) => Response | Promise<Response>

function stub(routes: Record<string, Handler>): URL[] {
  const asked: URL[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    asked.push(url)
    const route = routes[url.pathname]
    if (route) return route(url)
    return Response.json(
      { message: `unexpected ${url.pathname}` },
      { status: 404 }
    )
  })
  return asked
}

function open(path: string) {
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: [path] })
  )
  render(
    <ClientProvider client={forgeClient}>
      <RouterProvider router={router} />
    </ClientProvider>
  )
  return router
}

const projects: Handler = () =>
  Response.json({
    projects: [
      {
        id: "proj_1",
        name: "Checkout",
        slug: "checkout",
        org_id: "org",
        color: "",
        icon: "",
        description: "",
        created_at: "2026-10-01T00:00:00Z",
        created_by: "u",
        updated_at: "2026-10-01T00:00:00Z",
        exclusions: [],
        repositories: [],
        status: "ready",
      },
    ],
  })

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  setFeatures({ ai: true })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  setFeatures({ ai: false })
  access.edit = true
})

it("redirects /ai/models to / when AI is off", async () => {
  setFeatures({ ai: false })
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/ai/models"] })
  )
  await router.load()
  expect(router.state.location.pathname).toBe("/")
})

it("filters by task, size and license from the URL", async () => {
  const asked = stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  const router = open("/ai/models?task=code&size=small&license=apache-2.0")
  expect(
    await screen.findByRole("article", { name: "Qwen2.5 7B Instruct" })
  ).toBeInTheDocument()

  const filtered = () =>
    asked
      .filter((u) => u.pathname === "/api/v1/ai/models/registry" && u.search)
      .map((u) => Object.fromEntries(u.searchParams))
  expect(filtered()).toContainEqual({
    task: "code",
    size: "small",
    license: "apache-2.0",
  })
  expect(screen.getByRole("button", { name: /^Code/ })).toHaveAttribute(
    "aria-pressed",
    "true"
  )
  expect(screen.getByRole("combobox", { name: "Size" })).toHaveTextContent(
    "4B to 15B"
  )
  expect(screen.getByRole("combobox", { name: "License" })).toHaveTextContent(
    "Apache 2.0"
  )

  // Choosing a filter writes it to the URL, and the list follows it.
  await userEvent.click(screen.getByRole("switch", { name: "Tested only" }))
  await waitFor(() =>
    expect(router.state.location.search).toEqual({
      task: "code",
      size: "small",
      license: "apache-2.0",
      tested: true,
    })
  )
  await waitFor(() =>
    expect(filtered()).toContainEqual({
      task: "code",
      size: "small",
      license: "apache-2.0",
      tested: "true",
    })
  )
  // Pressing the chosen task again clears it.
  await userEvent.click(screen.getByRole("button", { name: /^Code/ }))
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty("task")
  )
})

it("says when your GPUs couldn't be checked", async () => {
  stub({
    "/api/v1/ai/models/registry": () =>
      Response.json({ ...registry, fits_notice: "Couldn't check your GPUs." }),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  open("/ai/models?fits=true")
  expect(
    await screen.findByText(/^Couldn't check your GPUs\./)
  ).toBeInTheDocument()
  expect(screen.getByRole("switch", { name: "Fits my GPUs" })).toBeChecked()
  // The list still shows, unfiltered by fit.
  expect(
    screen.getByRole("article", { name: "Llama 3.1 8B Instruct" })
  ).toBeInTheDocument()
})

function status(over: Record<string, unknown>) {
  return {
    can_edit: true,
    connection_id: "conn_1",
    connection_name: "gpu-box",
    cost: {
      estimate: false,
      hourly_usd: "",
      known: false,
      monthly_usd: "",
      share: "",
      source: "unknown",
    },
    engine_version: "0.31.0",
    entry: entry(),
    environments: [
      { environment: "production", environment_id: "env_1", mode: "home" },
    ],
    kind: "model-vllm",
    linked: [],
    max_model_len: 32768,
    month_to_date_usd: "0.0000",
    project_id: "proj_1",
    projected_usd: "",
    resource: "qwen",
    state: "ready",
    update_available: false,
    verification: {
      gpu_kind: "nvidia-l40s",
      need: { gpus: 1, max_model_len: 32768, per_gpu_gib: 18 },
      status: "tested",
    },
    deployment: {
      id: "aimd_1",
      accrued_at: "2026-10-27T00:00:00Z",
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-27T00:00:00Z",
      engine: "vllm",
      entry_id: "Qwen/Qwen2.5-7B-Instruct",
      environment_id: "env_1",
      month: "2026-10",
      month_cost_usd: "0.0000",
      phase: "active",
      project_id: "proj_1",
      quantization: "bf16",
      resource: "qwen",
      revision: 1,
    },
    ...over,
  }
}

it("sums GPU cost on the Deployed tab and counts unknown prices apart", async () => {
  stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/projects": projects,
    "/api/v1/ai/models/deployments": () =>
      Response.json({
        models: [
          status({
            gpu: { kind: "nvidia-l40s", count: 1 },
            cost: {
              estimate: false,
              hourly_usd: "1.5700",
              known: true,
              monthly_usd: "1146.10",
              share: "1 of 1 GPU",
              source: "server",
            },
            month_to_date_usd: "40.2500",
          }),
          status({
            gpu: { kind: "nvidia-l4", count: 1 },
            cost: {
              estimate: true,
              hourly_usd: "0.4300",
              known: true,
              monthly_usd: "313.90",
              share: "1 of 4 GPUs",
              source: "cluster",
            },
            month_to_date_usd: "3.0000",
            deployment: undefined,
            resource: "handmade",
            environments: [
              {
                environment: "staging",
                environment_id: "env_2",
                mode: "home",
              },
            ],
          }),
          status({
            gpu: { kind: "nvidia-h100-80gb", count: 2 },
            state: "waiting_for_server",
          }),
          status({
            entry: entry({ id: "BAAI/bge-small-en-v1.5", title: "BGE small" }),
            cost: {
              estimate: false,
              hourly_usd: "0.00",
              known: true,
              monthly_usd: "0.00",
              share: "",
              source: "cpu",
            },
            connection_name: "build-box",
          }),
        ],
      }),
  })
  open("/ai/models?tab=deployed")
  expect(
    await screen.findByText(
      "GPU models cost about $2.00/hr together, plus 1 with an unknown price. Some are estimates from list prices."
    )
  ).toBeInTheDocument()
  const rows = within(
    screen.getByRole("list", { name: "Deployed models" })
  ).getAllByRole("listitem")
  expect(rows).toHaveLength(4)

  const [served, estimated, waiting, cpu] = rows
  expect(
    within(served).getByRole("link", { name: "Qwen2.5 7B Instruct" })
  ).toHaveAttribute("href", "/projects/proj_1/catalog/qwen?environment=env_1")
  await waitFor(() => expect(served).toHaveTextContent("Checkout / production"))
  expect(served).toHaveTextContent("1× NVIDIA L40S on gpu-box")
  expect(served).toHaveTextContent("Ready")
  expect(served).toHaveTextContent("$1.57/hr")
  expect(served).toHaveTextContent("$40.25")
  expect(estimated).toHaveTextContent("~$0.43/hr")
  expect(estimated).toHaveTextContent("estimate")
  // A model added to the manifest by hand names and links its project too.
  expect(estimated).toHaveTextContent(
    "Checkout / staging, added to the manifest by hand"
  )
  expect(
    within(estimated).getByRole("link", { name: "Qwen2.5 7B Instruct" })
  ).toHaveAttribute(
    "href",
    "/projects/proj_1/catalog/handmade?environment=env_2"
  )
  expect(waiting).toHaveTextContent("Waiting for a server")
  expect(waiting).toHaveTextContent("unknown")
  expect(cpu).toHaveTextContent("Runs on CPU. Its cost is part of build-box.")
  // Unknown is never zero, and a CPU model isn't free.
  expect(document.body.textContent).not.toContain("$0.00")
})

it("renders loading, denied and failed distinctly", async () => {
  let reply: () => Response | Promise<Response> = () =>
    new Promise<Response>(() => {})
  stub({
    "/api/v1/ai/models/registry": () => reply(),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  open("/ai/models")
  expect(await screen.findByTestId("models-loading")).toBeInTheDocument()
  expect(screen.queryByRole("alert")).toBeNull()
  cleanup()
  forgeClient.clear()

  reply = () =>
    Response.json({ code: "forbidden", message: "forbidden" }, { status: 403 })
  open("/ai/models")
  expect(await screen.findByText("Models are restricted")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  cleanup()
  forgeClient.clear()

  reply = () =>
    Response.json(
      {
        code: "unavailable",
        message: "Hugging Face didn't answer. Try again in a minute.",
      },
      { status: 502 }
    )
  open("/ai/models")
  expect(
    await screen.findByText(
      "Hugging Face didn't answer. Try again in a minute."
    )
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  expect(screen.queryByText("Models are restricted")).toBeNull()
})

it("shows a shared empty state when nothing is deployed", async () => {
  stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/projects": projects,
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  const router = open("/ai/models?tab=deployed")
  expect(await screen.findByText("No models deployed yet")).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole("button", { name: "Browse the registry" })
  )
  await waitFor(() => expect(router.state.location.search).toEqual({}))
})

function registryAsks(asked: URL[]) {
  return asked
    .filter((u) => u.pathname === "/api/v1/ai/models/registry")
    .map((u) => Object.fromEntries(u.searchParams))
}

it("searches once typing stops, and keeps the search in the URL", async () => {
  const asked = stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  const router = open("/ai/models")
  const box = await screen.findByRole("searchbox", { name: "Search models" })
  await userEvent.type(box, "llama ")
  await waitFor(() =>
    expect(registryAsks(asked)).toContainEqual({ q: "llama" })
  )
  // One request for the whole word, after the pause, not one per key.
  await new Promise((r) => setTimeout(r, 400))
  expect(registryAsks(asked).filter((q) => "q" in q)).toEqual([{ q: "llama" }])
  expect(router.state.location.search).toEqual({ q: "llama " })
  expect(box).toHaveValue("llama ")
})

it("restores the search and switches from the URL", async () => {
  const asked = stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  open("/ai/models?q=llama&commercial=true")
  expect(
    await screen.findByRole("searchbox", { name: "Search models" })
  ).toHaveValue("llama")
  expect(
    screen.getByRole("switch", { name: "Commercial use only" })
  ).toBeChecked()
  await waitFor(() =>
    expect(registryAsks(asked)).toContainEqual({
      q: "llama",
      commercial: "true",
    })
  )
})

it("shows a member the registry without the ways to deploy", async () => {
  access.edit = false
  stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  })
  open("/ai/models")
  const card = await screen.findByRole("article", {
    name: "Qwen2.5 7B Instruct",
  })
  expect(
    screen.queryByRole("region", { name: "Any Hugging Face model" })
  ).toBeNull()
  const deploy = within(card).getByRole("button", { name: "Deploy" })
  expect(deploy).toBeDisabled()
  expect(deploy).toHaveAttribute(
    "title",
    "Ask a workspace admin for permission to deploy models."
  )
  expect(
    screen.getByText("Ask a workspace admin for permission to deploy models.")
  ).toBeInTheDocument()
})

it("says why a deployed model failed", async () => {
  stub({
    "/api/v1/ai/models/registry": () => Response.json(registry),
    "/api/v1/projects": projects,
    "/api/v1/ai/models/deployments": () =>
      Response.json({
        models: [
          status({
            state: "failed",
            gpu: { kind: "nvidia-l4", count: 1 },
            message: "The engine ran out of GPU memory loading the weights.",
            hint: "Pick a smaller quantization, then retry.",
          }),
        ],
      }),
  })
  open("/ai/models?tab=deployed")
  const reason = await screen.findByText(
    "The engine ran out of GPU memory loading the weights."
  )
  expect(reason).toHaveAttribute(
    "title",
    "The engine ran out of GPU memory loading the weights. Pick a smaller quantization, then retry."
  )
  // Labelled at every width, for a screen reader on a wide screen too.
  const row = reason.closest("li") as HTMLElement
  expect(within(row).getByText("This month")).toBeInTheDocument()
})

it("opens the deploy dialog from a card, and links the card to its page", async () => {
  stub({
    ...dialogRoutes(),
    "/api/v1/ai/models/registry": () => Response.json(registry),
  })
  open("/ai/models")
  const card = await screen.findByRole("article", {
    name: "Qwen2.5 7B Instruct",
  })
  expect(
    within(card).getByRole("link", { name: "Qwen2.5 7B Instruct" })
  ).toHaveAttribute("href", "/ai/models/Qwen/Qwen2.5-7B-Instruct")

  await userEvent.click(within(card).getByRole("button", { name: "Deploy" }))
  const dialog = await screen.findByRole("dialog", {
    name: "Deploy Qwen2.5 7B Instruct",
  })
  // It loads the entry it was opened for.
  expect(
    await within(dialog).findByRole("radio", { name: /bf16/ })
  ).toBeInTheDocument()
  await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
})
