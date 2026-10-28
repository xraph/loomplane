import { act, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setFeatures } from "@/lib/features"
import {
  deployPath,
  dialogRoutes,
  modelPath,
  modelRoutes,
  modelStatus,
  open,
  operation,
  preview,
  previewPath,
  qwen,
  stub,
  summary,
  type Handler,
} from "@/test-fixtures/ai-models"

// Each test says which permissions the person has.
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    can: (permission: string) => perms.granted.has(permission),
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => null }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))
// The pages a deploy navigates to, without everything they load.
vi.mock("@/pages/deployment", () => ({
  DeploymentPage: () => <h1>The plan</h1>,
}))

const admin = ["ai:read", "ai:edit", "ai:manage", "organization_cloud:manage"]
const developer = ["ai:read", "ai:edit"]

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  setFeatures({ ai: true })
  perms.granted = new Set(admin)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  setFeatures({ ai: false })
})

type Body = {
  target: { source: string; connection_id: string; region?: string }
  quantization?: string
  review_server?: boolean
  server?: unknown
}

// previewOf answers a preview the way the server does for the fixtures'
// targets: a server size waits for its review, which only review_server
// makes.
function previewOf(body: Body, over: Record<string, unknown> = {}) {
  if (body.target.source === "server_size") {
    const name = "checkout-qwen2-5-7b-instruct-staging-gpu"
    return body.review_server
      ? preview({
          target: body.target,
          target_name: name,
          server: operation({
            input: { ...operation().input, region: body.target.region },
          }),
          ...over,
        })
      : preview({
          target: body.target,
          target_name: name,
          can_deploy: false,
          server_review_needed: true,
          ...over,
        })
  }
  return preview({ target: body.target, ...over })
}

function routes(extra: Record<string, Handler> = {}) {
  return stub({
    ...dialogRoutes(),
    [previewPath]: async (_url, init) =>
      Response.json(previewOf(JSON.parse(String(init?.body)) as Body)),
    ...extra,
  })
}

async function openDialog() {
  const router = open(`/ai/models/${qwen}`)
  await userEvent.click(await screen.findByRole("button", { name: "Deploy" }))
  const dialog = await screen.findByRole("dialog")
  return { router, dialog }
}

function previews(asked: ReturnType<typeof stub>) {
  return asked
    .filter((a) => a.method === "POST" && a.url.pathname.endsWith("/preview"))
    .map((a) => a.body as Body)
}

it("lists only fitting GPU targets with their price", async () => {
  const asked = routes()
  const { dialog } = await openDialog()
  const fitting = await within(dialog).findByRole("button", {
    name: "Choose gpu-box",
  })
  const row = fitting.closest("tr")!
  expect(row).toHaveTextContent("$1.57/hr")
  expect(row).toHaveTextContent("about $1,146/mo")

  // Too little memory: listed with the reason, never choosable.
  const t4 = within(dialog).getByText("t4-box").closest("tr")!
  expect(t4).toHaveTextContent("Its T4 has 16 GB, and this needs 18 GB.")
  expect(within(t4).queryByRole("button")).toBeNull()

  // While no quantization is picked, the list asks for the smallest that
  // fits: the GGUF's 6 GB from its fits, since llama.cpp has no
  // min_gpu_memory_gib. One GPU each.
  const gpuQueries = () =>
    asked
      .filter((a) => a.url.pathname === "/api/v1/gpu-targets")
      .map((a) => Object.fromEntries(a.url.searchParams))
  expect(gpuQueries()).toContainEqual({ min_memory_gib: "6", count: "1" })

  // Picking bf16 asks again with its memory, and the A10 it failed on in
  // qualification can't be chosen.
  await userEvent.click(within(dialog).getByRole("radio", { name: /bf16/ }))
  await waitFor(() =>
    expect(gpuQueries()).toContainEqual({ min_memory_gib: "18", count: "1" })
  )
  const a10 = within(dialog).getByText("a10-box").closest("tr")!
  expect(a10).toHaveTextContent(
    "Loomplane's qualification couldn't serve it on NVIDIA A10."
  )
  expect(within(a10).queryByRole("button")).toBeNull()
  // Nothing was previewed without a target.
  expect(previews(asked)).toHaveLength(0)
})

it("defaults to the best quantization that fits and shows its context limit", async () => {
  const asked = routes()
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  expect(
    await within(dialog).findByText("Up to 32,768 tokens fit on NVIDIA L40S.")
  ).toBeInTheDocument()
  // The server picked it: the request named none.
  expect(previews(asked)[0]).not.toHaveProperty("quantization")
  expect(previews(asked)[0].target).toEqual({
    source: "connection",
    connection_id: "conn_gpu",
    gpu: { kind: "nvidia-l40s", count: 1 },
  })
  const bf16 = within(dialog).getByRole("radio", { name: /bf16/ })
  expect(bf16).toBeChecked()
  expect(
    within(dialog).getByText(/bf16 is the best that fits/)
  ).toBeInTheDocument()
  // Its fit on the chosen GPU, tested and saying so.
  expect(
    within(bf16.closest("label")!).getByRole("button", {
      name: /^Tested on NVIDIA L40S/,
    })
  ).toBeInTheDocument()
  expect(within(dialog).getByTestId("deploy-cost")).toHaveTextContent(
    "$1.57/hr, about $1,146/mo while it runs"
  )
})

it("explains reach per environment", async () => {
  routes()
  const { dialog } = await openDialog()
  // Each environment runs its own copy, and only its services link to it.
  expect(
    await within(dialog).findByText(
      "Runs in staging. Services in staging can link to it."
    )
  ).toBeInTheDocument()
  expect(within(dialog).queryByRole("switch")).toBeNull()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  // The footer's one reach line is the server's.
  expect(
    await within(dialog).findByText(
      "Runs in staging. Services there link to it and get MODEL_URL and MODEL_API_KEY."
    )
  ).toBeInTheDocument()
})

it("says when you can't deploy to production", async () => {
  perms.granted = new Set(developer)
  routes({
    "POST /api/v1/projects/proj_1/environments/env_prod/ai/models/preview":
      () =>
        Response.json(
          preview({
            can_deploy: false,
            environments: [
              {
                environment: "production",
                environment_id: "env_prod",
                mode: "home",
                message: "Runs in production.",
              },
            ],
          })
        ),
  })
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("combobox", { name: "Environment" })
  )
  await userEvent.click(
    await screen.findByRole("option", { name: "production" })
  )
  expect(
    await within(dialog).findByText(
      "Only a workspace admin can deploy models to production. Ask one, or pick another environment."
    )
  ).toBeInTheDocument()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  await within(dialog).findByText("Runs in production.")
  expect(within(dialog).getByRole("button", { name: "Plan it" })).toBeDisabled()
})

it("blocks a gated model without a token and links to settings for admins", async () => {
  const gated = {
    gated: true,
    token_ready: false,
    can_deploy: false,
    entry: summary({ gated: true }),
    problems: [
      {
        code: "model-gated",
        message:
          "Qwen2.5 7B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings.",
      },
    ],
  }
  routes({
    [previewPath]: () => Response.json(preview(gated)),
  })
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  expect(
    await within(dialog).findByText(/is gated on Hugging Face/)
  ).toBeInTheDocument()
  expect(
    within(dialog).getByRole("link", { name: "Open AI settings" })
  ).toHaveAttribute("href", "/ai/settings")
  expect(within(dialog).getByRole("button", { name: "Plan it" })).toBeDisabled()
})

it("tells a developer to ask an admin for the Hugging Face token", async () => {
  perms.granted = new Set(developer)
  routes({
    [previewPath]: () =>
      Response.json(
        preview({
          gated: true,
          can_deploy: false,
          problems: [
            {
              code: "model-gated",
              message: "Add a Hugging Face token in AI settings.",
            },
          ],
        })
      ),
  })
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  expect(
    await within(dialog).findByText(
      "Ask a workspace admin to add a Hugging Face token in AI settings."
    )
  ).toBeInTheDocument()
  expect(
    within(dialog).queryByRole("link", { name: "Open AI settings" })
  ).toBeNull()
})

async function chooseServerSize(dialog: HTMLElement) {
  await userEvent.click(
    await within(dialog).findByRole("button", {
      name: "Choose gpu-l40sx1-48gb",
    })
  )
}

it("creates a GPU server first for admins and shows its review", async () => {
  const asked = routes({
    [deployPath]: () =>
      Response.json(
        {
          deployment: {
            id: "aimodel_1",
            phase: "waiting_for_server",
            project_id: "proj_1",
            environment_id: "env_stg",
            resource: "qwen2-5-7b-instruct",
          },
        },
        { status: 201 }
      ),
  })
  const { dialog, router } = await openDialog()
  await chooseServerSize(dialog)
  // The region the server is created in, first of the size's.
  expect(
    within(dialog).getByRole("combobox", { name: "Region" })
  ).toHaveTextContent("tor1")
  const create = within(dialog).getByRole("button", {
    name: "Create the server and plan it",
  })
  const review = await within(dialog).findByRole("button", {
    name: "Review server",
  })
  await waitFor(() => expect(review).toBeEnabled())
  expect(create).toBeDisabled()
  // A plain preview never reviews the server.
  expect(previews(asked).every((b) => !b.review_server)).toBe(true)

  await userEvent.click(review)
  expect(await within(dialog).findByTestId("server-review")).toHaveTextContent(
    "Create checkout-qwen2-5-7b-instruct-staging-gpu in tor1 · $1.57/hr"
  )
  expect(dialog).toHaveTextContent(
    "It bills from the moment it's created until you delete it in Infrastructure"
  )
  const reviews = previews(asked).filter((b) => b.review_server)
  expect(reviews).toHaveLength(1)
  expect(reviews[0].target).toMatchObject({
    source: "server_size",
    connection_id: "conn_do",
    region: "tor1",
    size_id: "gpu-l40sx1-48gb",
  })

  await waitFor(() => expect(create).toBeEnabled())
  await userEvent.click(create)
  // A model waiting for its server goes to its own page.
  await waitFor(() =>
    expect(router.state.location.pathname).toBe(
      "/projects/proj_1/ai/models/qwen2-5-7b-instruct"
    )
  )
  expect(router.state.location.search).toEqual({ environment: "env_stg" })
  const sent = asked.find(
    (a) => a.method === "POST" && a.url.pathname.endsWith("/ai/models")
  )
  expect(sent?.body).toMatchObject({
    server: { cloud_id: "cloud_1", operation_id: "op_1", review: "review_1" },
  })
  expect(sent?.body).not.toHaveProperty("review_server")
})

it("never prices a reviewed server it doesn't know at $0", async () => {
  routes({
    [previewPath]: async (_url, init) => {
      const body = JSON.parse(String(init?.body)) as Body
      return Response.json(
        previewOf(body, {
          cost: {
            estimate: false,
            hourly_usd: "",
            known: false,
            monthly_usd: "",
            share: "",
            source: "unknown",
          },
          ...(body.review_server
            ? {
                server: operation({
                  estimated_hourly_usd: 0,
                  hourly_price_known: false,
                }),
              }
            : {}),
        })
      )
    },
  })
  const { dialog } = await openDialog()
  await chooseServerSize(dialog)
  const review = await within(dialog).findByRole("button", {
    name: "Review server",
  })
  await waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  expect(await within(dialog).findByTestId("server-review")).toHaveTextContent(
    "· Price unknown"
  )
  expect(within(dialog).getByTestId("deploy-cost")).toHaveTextContent(
    "Price unknown"
  )
  expect(dialog.textContent).not.toContain("$0")
})

it("disables server creation for developers with the reason", async () => {
  perms.granted = new Set(developer)
  const asked = routes({
    [previewPath]: async (_url, init) =>
      Response.json(
        previewOf(JSON.parse(String(init?.body)) as Body, {
          problems: [
            {
              code: "model-server-needs-admin",
              message:
                "An admin creates GPU servers. Ask one to deploy this, or pick a server you have.",
            },
          ],
          server_review_needed: false,
        })
      ),
  })
  const { dialog } = await openDialog()
  await chooseServerSize(dialog)
  await waitFor(() => expect(previews(asked)).toHaveLength(1))
  expect(
    await within(dialog).findByText(
      "An admin creates GPU servers. Ask one to deploy this, or pick a server you have."
    )
  ).toBeInTheDocument()
  expect(
    within(dialog).getByRole("button", { name: "Review server" })
  ).toBeDisabled()
  expect(
    within(dialog).getByRole("button", {
      name: "Create the server and plan it",
    })
  ).toBeDisabled()
  expect(previews(asked).some((b) => b.review_server)).toBe(false)
})

it("plans and navigates to the plan", async () => {
  const asked = routes({
    [deployPath]: () =>
      Response.json(
        {
          plan_id: "plan_1",
          deployment: { id: "aimodel_1", phase: "planned", resource: "qwen" },
        },
        { status: 201 }
      ),
  })
  const { dialog, router } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  await within(dialog).findByTestId("deploy-cost")
  // Budget and context go with it.
  await userEvent.type(
    within(dialog).getByLabelText("Monthly budget in US dollars"),
    "250"
  )
  const plan = within(dialog).getByRole("button", { name: "Plan it" })
  await waitFor(() => expect(plan).toBeEnabled())
  await userEvent.click(plan)
  await waitFor(() =>
    expect(router.state.location.pathname).toBe("/deployments/plan_1")
  )
  const sent = asked.find(
    (a) => a.method === "POST" && a.url.pathname.endsWith("/ai/models")
  )
  expect(sent?.body).toEqual({
    entry_id: qwen,
    target: {
      source: "connection",
      connection_id: "conn_gpu",
      gpu: { kind: "nvidia-l40s", count: 1 },
    },
    monthly_budget_usd: "250",
  })
})

async function reviewAndCreate(dialog: HTMLElement) {
  await chooseServerSize(dialog)
  const review = await within(dialog).findByRole("button", {
    name: "Review server",
  })
  await waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  await within(dialog).findByTestId("server-review")
  const create = within(dialog).getByRole("button", {
    name: "Create the server and plan it",
  })
  await waitFor(() => expect(create).toBeEnabled())
  await userEvent.click(create)
}

it("says when the capacity policy holds the server back", async () => {
  const held =
    "The GPU server waits for an admin to approve it in the cloud's capacity policy."
  routes({
    [deployPath]: () =>
      Response.json(
        {
          deployment: {
            id: "aimodel_1",
            phase: "waiting_for_server",
            message: held,
          },
        },
        { status: 201 }
      ),
  })
  const { dialog } = await openDialog()
  await reviewAndCreate(dialog)
  expect(await within(dialog).findByText(held)).toBeInTheDocument()
  expect(within(dialog).getByText("Waiting for a server")).toBeInTheDocument()
  expect(
    within(dialog).getByRole("link", { name: "Open the Cloud's Capacity tab" })
  ).toHaveAttribute("href", "/clouds/cloud_1?tab=capacity")
})

it("says why the capacity policy refused the server", async () => {
  const refused =
    "The GPU server would take the cloud past its capacity limit of 2 NVIDIA L40S GPUs: 2 are counted and it needs 1 more. An admin can raise the limit, then deploy the model again."
  routes({
    [deployPath]: () =>
      Response.json(
        { code: "model-capacity", message: refused },
        { status: 422 }
      ),
  })
  const { dialog } = await openDialog()
  await reviewAndCreate(dialog)
  expect(await within(dialog).findByText(refused)).toBeInTheDocument()
  expect(
    within(dialog).getByRole("link", { name: "Open the Cloud's Capacity tab" })
  ).toHaveAttribute("href", "/clouds/cloud_1?tab=capacity")
  // The review was spent: the next try reviews again.
  expect(
    within(dialog).getByRole("button", { name: "Review server" })
  ).toBeInTheDocument()
})

it("runs a CPU engine on a ready server and says its cost is the server's", async () => {
  const asked = routes({
    [previewPath]: async (_url, init) => {
      const body = JSON.parse(String(init?.body)) as Body
      return Response.json(
        preview({
          target: body.target,
          target_name: "build-box",
          gpu: undefined,
          kind: "model-llamacpp",
          quantization: "gguf-q4_k_m",
          fit: {
            gpu_kind: "cpu",
            need: { gpus: 0, max_model_len: 32768, per_gpu_gib: 0, ram_gib: 8 },
            status: "estimated",
          },
          cost: {
            estimate: false,
            hourly_usd: "0.00",
            known: true,
            monthly_usd: "0.00",
            share: "",
            source: "cpu",
          },
        })
      )
    },
  })
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("radio", { name: "CPU" })
  )
  const servers = within(dialog).getByRole("radiogroup", {
    name: "Server or cluster",
  })
  // Ready servers and clusters only.
  expect(
    within(servers)
      .getAllByRole("radio")
      .map((r) => r.closest("label")?.textContent)
  ).toEqual(["gpu-boxSSH server", "build-boxSSH server"])
  // Only the engines that run on CPU.
  expect(within(dialog).queryByRole("radio", { name: /bf16/ })).toBeNull()
  await userEvent.click(
    within(servers).getByRole("radio", { name: /build-box/ })
  )
  expect(
    await within(dialog).findByText(
      "Runs on CPU. Its cost is part of build-box."
    )
  ).toBeInTheDocument()
  expect(dialog.textContent).not.toContain("$0")
  expect(previews(asked).at(-1)?.target).toEqual({
    source: "connection",
    connection_id: "conn_cpu",
  })
})

// reviewsNumbered answers each review with a new operation, op_1, op_2 and
// so on, the way the server records each one.
function reviewsNumbered(): Handler {
  let n = 0
  return async (_url, init) => {
    const body = JSON.parse(String(init?.body)) as Body
    if (!body.review_server) return Response.json(previewOf(body))
    n++
    return Response.json(
      previewOf(body, {
        server: operation({ id: `op_${n}`, review: `review_${n}` }),
      })
    )
  }
}

function deploys(asked: ReturnType<typeof stub>) {
  return asked
    .filter((a) => a.method === "POST" && a.url.pathname.endsWith("/ai/models"))
    .map((a) => a.body as Body)
}

it("recovers from a review that expired before the deploy", async () => {
  const stale =
    "The GPU server's review is more than five minutes old. Preview the deploy again."
  let tries = 0
  const asked = routes({
    [previewPath]: reviewsNumbered(),
    [deployPath]: () => {
      tries++
      return tries === 1
        ? Response.json(
            { code: "model-server-review", message: stale },
            { status: 422 }
          )
        : Response.json(
            {
              deployment: {
                id: "aimodel_1",
                phase: "waiting_for_server",
                project_id: "proj_1",
                environment_id: "env_stg",
                resource: "qwen2-5-7b-instruct",
              },
            },
            { status: 201 }
          )
    },
  })
  const { dialog } = await openDialog()
  await reviewAndCreate(dialog)

  // The server's sentence, what to press, and the old review gone.
  expect(await within(dialog).findByText(stale)).toBeInTheDocument()
  expect(
    within(dialog).getByText("Review the server again.")
  ).toBeInTheDocument()
  expect(within(dialog).queryByTestId("server-review")).toBeNull()
  const create = within(dialog).getByRole("button", {
    name: "Create the server and plan it",
  })
  expect(create).toBeDisabled()
  const review = within(dialog).getByRole("button", { name: "Review server" })
  await waitFor(() => expect(review).toBeEnabled())

  // A new review clears the refusal, and the deploy carries it.
  await userEvent.click(review)
  await within(dialog).findByTestId("server-review")
  expect(within(dialog).queryByText(stale)).toBeNull()
  expect(within(dialog).queryByText("Review the server again.")).toBeNull()
  await waitFor(() => expect(create).toBeEnabled())
  await userEvent.click(create)
  await waitFor(() => expect(deploys(asked)).toHaveLength(2))
  expect(deploys(asked).map((b) => b.server)).toEqual([
    { cloud_id: "cloud_1", operation_id: "op_1", review: "review_1" },
    { cloud_id: "cloud_1", operation_id: "op_2", review: "review_2" },
  ])
})

it("clears what the last deploy said once the server is reviewed again", async () => {
  const refused =
    "The GPU server would take the cloud past its capacity limit of 2 NVIDIA L40S GPUs: 2 are counted and it needs 1 more. An admin can raise the limit, then deploy the model again."
  routes({
    [previewPath]: reviewsNumbered(),
    [deployPath]: () =>
      Response.json(
        { code: "model-capacity", message: refused },
        { status: 422 }
      ),
  })
  const { dialog } = await openDialog()
  await reviewAndCreate(dialog)
  expect(await within(dialog).findByText(refused)).toBeInTheDocument()
  const review = within(dialog).getByRole("button", { name: "Review server" })
  await waitFor(() => expect(review).toBeEnabled())
  await userEvent.click(review)
  await within(dialog).findByTestId("server-review")
  expect(within(dialog).queryByText(refused)).toBeNull()
  expect(
    within(dialog).queryByRole("link", {
      name: "Open the Cloud's Capacity tab",
    })
  ).toBeNull()
})

it("sends one review and one deploy for two quick clicks", async () => {
  let release = () => {}
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  const asked = routes({
    [previewPath]: reviewsNumbered(),
    [deployPath]: async () => {
      await held
      return Response.json(
        {
          plan_id: "plan_1",
          deployment: { id: "aimodel_1", phase: "planned" },
        },
        { status: 201 }
      )
    },
  })
  const { dialog } = await openDialog()
  await chooseServerSize(dialog)
  const review = await within(dialog).findByRole("button", {
    name: "Review server",
  })
  await waitFor(() => expect(review).toBeEnabled())
  // Both clicks land before React draws the first one's state.
  act(() => {
    review.click()
    review.click()
  })
  await within(dialog).findByTestId("server-review")
  expect(previews(asked).filter((b) => b.review_server)).toHaveLength(1)

  const create = within(dialog).getByRole("button", {
    name: "Create the server and plan it",
  })
  await waitFor(() => expect(create).toBeEnabled())
  act(() => {
    create.click()
    create.click()
  })
  release()
  await waitFor(() => expect(deploys(asked)).toHaveLength(1))
  // Nothing else follows once the first has answered.
  await new Promise((resolve) => setTimeout(resolve, 50))
  expect(deploys(asked)).toHaveLength(1)
})

it("says the server may already be approved when the deploy fails after it", async () => {
  routes({
    [previewPath]: reviewsNumbered(),
    [deployPath]: () =>
      Response.json({ message: "internal error" }, { status: 500 }),
  })
  const { dialog } = await openDialog()
  await reviewAndCreate(dialog)
  expect(
    await within(dialog).findByText(
      "Loomplane couldn't confirm the deploy. The GPU server may already be approved and on its way."
    )
  ).toBeInTheDocument()
  expect(
    within(dialog).getByText("Check the model page before you deploy again.")
  ).toBeInTheDocument()
  expect(dialog).not.toHaveTextContent("The model wasn't deployed.")
})

it("says a deploy without a new server wasn't made when it fails", async () => {
  routes({
    [deployPath]: () =>
      Response.json({ message: "internal error" }, { status: 500 }),
  })
  const { dialog } = await openDialog()
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  const plan = within(dialog).getByRole("button", { name: "Plan it" })
  await waitFor(() => expect(plan).toBeEnabled())
  await userEvent.click(plan)
  expect(
    await within(dialog).findByText("The model wasn't deployed.")
  ).toBeInTheDocument()
  expect(dialog).not.toHaveTextContent("may already be approved")
})

// The model page's "Deploy to another environment", for a model that runs
// in staging: production gets its own copy of the same resource.
const prodPreviewPath =
  "POST /api/v1/projects/proj_1/environments/env_prod/ai/models/preview"

function addsTo(body: Body) {
  return preview({
    target: body.target,
    adds_to: "qwen2-5-7b-instruct",
    environments: [
      {
        environment: "production",
        environment_id: "env_prod",
        mode: "home",
        message:
          "Adds production to qwen2-5-7b-instruct. Services in production link to this copy.",
      },
      {
        environment: "staging",
        environment_id: "env_stg",
        mode: "home",
        message: "Runs in staging. Services in staging link to this copy.",
      },
    ],
  })
}

async function openAddDialog() {
  const asked = stub({
    ...modelRoutes(),
    [modelPath]: () => Response.json(modelStatus()),
    [prodPreviewPath]: async (_url, init) =>
      Response.json(addsTo(JSON.parse(String(init?.body)) as Body)),
  })
  open("/projects/proj_1/ai/models/qwen2-5-7b-instruct?environment=env_stg")
  await userEvent.click(
    await screen.findByRole("button", {
      name: "Deploy to another environment",
    })
  )
  const dialog = await screen.findByRole("dialog")
  return { asked, dialog }
}

it("adds an environment to a model that runs elsewhere", async () => {
  const { asked, dialog } = await openAddDialog()
  // Staging runs it already, so the dialog starts on production.
  expect(
    await within(dialog).findByRole("combobox", { name: "Environment" })
  ).toHaveTextContent("production")
  await userEvent.click(
    await within(dialog).findByRole("button", { name: "Choose gpu-box" })
  )
  expect(
    await within(dialog).findByText(
      "Adds production to qwen2-5-7b-instruct. It keeps running in staging. Each environment runs its own copy, on its own server, at its own cost."
    )
  ).toBeInTheDocument()
  // The server's sentence for production says the same, so it shows once.
  expect(
    within(dialog).queryByText(
      "Adds production to qwen2-5-7b-instruct. Services in production link to this copy."
    )
  ).toBeNull()
  const sent = previews(asked).at(-1) as Body & { resource?: string }
  expect(sent.resource).toBe("qwen2-5-7b-instruct")
  // Money rules hold: the cost shows, and Plan it waits for the preview.
  expect(within(dialog).getByTestId("deploy-cost")).toHaveTextContent("$1.57")
  await waitFor(() =>
    expect(
      within(dialog).getByRole("button", { name: "Plan it" })
    ).toBeEnabled()
  )
})

it("disables environments the model already runs in", async () => {
  const { dialog } = await openAddDialog()
  await userEvent.click(
    await within(dialog).findByRole("combobox", { name: "Environment" })
  )
  const staging = await screen.findByRole("option", {
    name: "staging (runs there)",
  })
  expect(staging).toHaveAttribute("aria-disabled", "true")
  await userEvent.click(staging)
  expect(
    within(dialog).getByRole("combobox", { name: "Environment" })
  ).toHaveTextContent("production")
})
