import { act, cleanup, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { setFeatures } from "@/lib/features"
import {
  deployment,
  knownCost,
  modelPath,
  modelRoutes,
  modelStatus,
  open,
  operation,
  preview,
  stub,
  summary,
  type Handler,
} from "@/test-fixtures/ai-models"

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
vi.mock("@/pages/deployment", () => ({
  DeploymentPage: () => <h1>The plan</h1>,
}))

const admin = [
  "ai:read",
  "ai:edit",
  "ai:manage",
  "organization_cloud:manage",
  "project:update",
]

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

const page =
  "/projects/proj_1/ai/models/qwen2-5-7b-instruct?environment=env_stg"

function routes(
  status: Record<string, unknown> = modelStatus(),
  extra: Record<string, Handler> = {}
) {
  return stub({
    ...modelRoutes(),
    [modelPath]: () => Response.json(status),
    ...extra,
  })
}

async function openModel(path = page) {
  const router = open(path)
  await screen.findByRole("heading", {
    name: /qwen2-5-7b-instruct/,
    level: 1,
  })
  return router
}

function fresh() {
  cleanup()
  forgeClient.clear()
  vi.unstubAllGlobals()
}

it("redirects a model's page to / when AI is off", async () => {
  setFeatures({ ai: false })
  routes()
  const router = open(page)
  await waitFor(() => expect(router.state.location.pathname).toBe("/"))
})

it("shows waiting for server, then planned, then ready", async () => {
  routes(
    modelStatus({
      state: "waiting_for_server",
      message:
        "The GPU server checkout-qwen-gpu is being created. The model deploys once it's up.",
      internal_url: undefined,
      deployment: deployment({
        phase: "waiting_for_server",
        server_name: "checkout-qwen-gpu",
      }),
    })
  )
  await openModel()
  const state = screen.getByRole("region", { name: "State" })
  expect(within(state).getByText("Waiting for a server")).toBeInTheDocument()
  expect(state).toHaveTextContent("checkout-qwen-gpu is being created")
  expect(within(state).getByRole("button", { name: "Cancel" })).toBeEnabled()

  fresh()
  routes(
    modelStatus({
      state: "planned",
      plan_id: "plan_9",
      internal_url: undefined,
    })
  )
  await openModel()
  const planned = screen.getByRole("region", { name: "State" })
  expect(within(planned).getByText("Planned")).toBeInTheDocument()
  expect(
    within(planned).getByRole("link", { name: "Open the plan" })
  ).toHaveAttribute("href", "/deployments/plan_9")

  fresh()
  routes(modelStatus({ state: "deploying", internal_url: undefined }))
  await openModel()
  expect(
    screen.getByText(
      "Downloading and loading weights. Big models take a while."
    )
  ).toBeInTheDocument()

  fresh()
  routes()
  await openModel()
  const ready = screen.getByRole("region", { name: "State" })
  expect(within(ready).getByText("Ready")).toBeInTheDocument()
  expect(ready).toHaveTextContent(
    "http://qwen2-5-7b-instruct.staging.internal:8080"
  )
  expect(within(ready).queryByRole("button")).toBeNull()
})

it("shows an unknown price as unknown", async () => {
  const asked = routes(
    modelStatus({
      cost: {
        estimate: false,
        hourly_usd: "",
        known: false,
        monthly_usd: "",
        share: "",
        source: "unknown",
      },
      month_to_date_usd: "0.0000",
      projected_usd: "",
      deployment: deployment({ month_cost_usd: "0.0000" }),
    }),
    {
      [`PATCH ${modelPath}`]: () => Response.json(modelStatus()),
    }
  )
  await openModel()
  const cost = screen.getByRole("region", { name: "Cost" })
  expect(within(cost).getByTestId("model-cost")).toHaveTextContent(
    "Price unknown"
  )
  expect(document.body.textContent).not.toMatch(/\$0(\.00)?\b/)

  // What the owner says it costs goes with the row's revision.
  await userEvent.type(
    within(cost).getByLabelText("It costs me per hour"),
    "1.20"
  )
  await userEvent.click(
    within(cost).getByRole("button", { name: "Save price" })
  )
  await waitFor(() =>
    expect(asked.find((a) => a.method === "PATCH")?.body).toEqual({
      hourly_usd_override: "1.20",
      revision: 3,
    })
  )
})

it("projects the monthly cost", async () => {
  routes()
  await openModel()
  const cost = within(screen.getByRole("region", { name: "Cost" })).getByTestId(
    "model-cost"
  )
  expect(cost).toHaveTextContent("$1.57/hr")
  expect(cost).toHaveTextContent("$120.50 so far")
  expect(cost).toHaveTextContent("$1,146 projected this month")
})

it("marks an estimate and says a CPU model's cost is its server's", async () => {
  routes(
    modelStatus({
      cost: { ...knownCost, estimate: true, source: "node" },
    })
  )
  await openModel()
  const cost = screen.getByTestId("model-cost")
  expect(cost).toHaveTextContent("~$1.57/hr")
  expect(cost).toHaveTextContent("estimate")

  fresh()
  routes(
    modelStatus({
      kind: "model-llamacpp",
      gpu: undefined,
      connection_name: "build-box",
      cost: {
        estimate: false,
        hourly_usd: "0.00",
        known: true,
        monthly_usd: "0.00",
        share: "",
        source: "cpu",
      },
      projected_usd: "",
    })
  )
  await openModel()
  expect(screen.getByTestId("model-cost")).toHaveTextContent(
    "Runs on CPU. Its cost is part of build-box."
  )
  expect(document.body.textContent).not.toContain("$0")
})

it("shows what the server it moves to costs while the move waits for it", async () => {
  routes(
    modelStatus({
      state: "waiting_for_server",
      move_cost: { ...knownCost, hourly_usd: "3.1400" },
      deployment: deployment({ phase: "waiting_for_server" }),
    })
  )
  await openModel()
  const move = screen.getByTestId("move-cost")
  expect(move).toHaveTextContent("the new GPU server costs $3.14/hr")
  expect(move).toHaveTextContent("both bill once the new server is up")
})

it("shows the out of memory hint on a failed start", async () => {
  routes(
    modelStatus({
      state: "failed",
      internal_url: undefined,
      message: "torch.OutOfMemoryError: CUDA out of memory.",
      hint: "The model didn't fit in GPU memory at 32768 tokens. Try a shorter context or a smaller quantization.",
    })
  )
  await openModel()
  const state = screen.getByRole("region", { name: "State" })
  expect(within(state).getByText("Failed")).toBeInTheDocument()
  expect(state).toHaveTextContent("CUDA out of memory")
  expect(state).toHaveTextContent(
    "The model didn't fit in GPU memory at 32768 tokens. Try a shorter context or a smaller quantization."
  )
  await userEvent.click(
    within(state).getByRole("button", {
      name: "Move it with a shorter context",
    })
  )
  expect(
    await screen.findByRole("region", { name: "Move" })
  ).toBeInTheDocument()
})

it("says the version before a failed change still serves", async () => {
  routes(
    modelStatus({
      state: "failed",
      message: "The engine exited.",
      hint: "The version deployed before is still running, so linked services keep working while you fix this.",
    })
  )
  await openModel()
  const state = screen.getByRole("region", { name: "State" })
  expect(within(state).getByText("Update failed")).toBeInTheDocument()
  expect(state).toHaveTextContent(
    "The change didn't start. The version deployed before is still running"
  )
  // Said once.
  expect(state.textContent?.match(/still running/g)).toHaveLength(1)
})

it("names the engine without claiming which release it runs", async () => {
  routes()
  await openModel()
  expect(
    screen.getByText("Loomplane's current vLLM release is 0.31.0")
  ).toBeInTheDocument()
})

it("moves to the new pin after asking, then links the plan", async () => {
  const asked = routes(
    modelStatus({
      update_available: true,
      pin: {
        update_available: true,
        model: "Qwen/Qwen2.5-7B-Instruct",
        revision: "c".repeat(40),
        message:
          "The model registry now pins Qwen/Qwen2.5-7B-Instruct bf16 to new weights. Move it to the new pin to plan the update.",
      },
    }),
    {
      [`POST ${modelPath}/move-to-pin`]: () =>
        Response.json({ plan_id: "plan_pin" }),
    }
  )
  await openModel()
  const banner = screen.getByRole("region", { name: "Update available" })
  await userEvent.click(
    within(banner).getByRole("button", {
      name: "Move to Qwen/Qwen2.5-7B-Instruct at ccccccc",
    })
  )
  const confirm = await screen.findByRole("alertdialog")
  expect(asked.some((a) => a.url.pathname.endsWith("/move-to-pin"))).toBe(false)
  await userEvent.click(
    within(confirm).getByRole("button", { name: "Plan the move" })
  )
  expect(
    await within(banner).findByRole("link", { name: "Open the plan" })
  ).toHaveAttribute("href", "/deployments/plan_pin")
})

it("lists linked services and why one can't reach it", async () => {
  const asked = routes(
    modelStatus({
      linked: [
        { environment: "staging", service: "checkout", reachable: true },
        {
          environment: "staging",
          service: "search",
          reachable: false,
          message:
            "search runs on edge-cluster, and qwen2-5-7b-instruct runs on gpu-box. Run them on the same server or cluster.",
        },
      ],
    }),
    {
      "POST /api/v1/projects/proj_1/manifest/edits": () =>
        Response.json({ revision: 5 }),
    }
  )
  await openModel()
  const section = screen.getByRole("region", { name: "Linked services" })
  const rows = within(section).getAllByRole("listitem")
  expect(rows[0]).toHaveTextContent("checkout")
  expect(rows[0]).toHaveTextContent("Reachable")
  expect(rows[1]).toHaveTextContent("Can't reach it")
  expect(rows[1]).toHaveTextContent(
    "search runs on edge-cluster, and qwen2-5-7b-instruct runs on gpu-box."
  )

  // Linking is the manifest's own edit, for a service that doesn't link yet.
  await userEvent.click(
    within(section).getByRole("button", { name: "Link a service" })
  )
  const link = within(section).getByRole("button", { name: "Link service" })
  await waitFor(() => expect(link).toBeEnabled())
  await userEvent.click(link)
  await waitFor(() =>
    expect(
      asked.find((a) => a.url.pathname.endsWith("/manifest/edits"))?.body
    ).toEqual({ link: { service: "checkout", need: "qwen2-5-7b-instruct" } })
  )
  expect(
    await within(section).findByText(
      /gets MODEL_URL and MODEL_API_KEY in staging/
    )
  ).toBeInTheDocument()
})

it("says who can change a production model", async () => {
  perms.granted = new Set(["ai:read", "ai:edit"])
  stub({
    ...modelRoutes(),
    "/api/v1/projects/proj_1/environments/env_prod/ai/models/qwen2-5-7b-instruct":
      () =>
        Response.json(
          modelStatus({
            can_edit: false,
            state: "needs_attention",
            message: "The GPU server failed to start.",
            environments: [
              {
                environment: "production",
                environment_id: "env_prod",
                mode: "home",
              },
            ],
            deployment: deployment({ phase: "needs_attention" }),
          })
        ),
  })
  await openModel(
    "/projects/proj_1/ai/models/qwen2-5-7b-instruct?environment=env_prod"
  )
  const state = screen.getByRole("region", { name: "State" })
  expect(within(state).getByRole("button", { name: "Retry" })).toBeDisabled()
  expect(within(state).getByRole("button", { name: "Cancel" })).toBeDisabled()
  expect(state).toHaveTextContent(
    "Only a workspace admin can change models in production."
  )
  expect(screen.getByLabelText("Budget alert per month")).toBeDisabled()
})

it("shows the server's sentence after a cancel", async () => {
  const approved =
    "checkout-qwen-gpu was already approved and is being created. Delete it in Infrastructure if you don't want it: it costs money once it's up."
  routes(
    modelStatus({
      state: "waiting_for_server",
      deployment: deployment({ phase: "waiting_for_server" }),
    }),
    {
      [`DELETE ${modelPath}/waiting`]: () =>
        Response.json({ message: approved }),
    }
  )
  await openModel()
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }))
  const notice = await screen.findByText(approved)
  expect(notice.closest("[role=status]")).toHaveTextContent("Canceled")
  expect(
    within(notice.closest("[role=status]") as HTMLElement).getByRole("link", {
      name: "Open Infrastructure",
    })
  ).toHaveAttribute("href", "/infrastructure")
})

it("shows MODEL_URL examples for each task", async () => {
  routes(
    modelStatus({
      entry: summary({
        tasks: ["chat", "embedding", "rerank", "speech-to-text"],
      }),
    })
  )
  await openModel(`${page}&tab=example`)
  expect(
    screen.getByText(/Services that link to/).closest("p")
  ).toHaveTextContent(
    "Services that link to qwen2-5-7b-instruct get MODEL_URL and MODEL_API_KEY."
  )
  const chat = screen.getByRole("region", { name: "Chat" })
  // The served name from the manifest, the base URL with /v1 and the key
  // from the environment.
  await waitFor(() =>
    expect(within(chat).getByLabelText("Chat in curl")).toHaveTextContent(
      '"model": "qwen-chat"'
    )
  )
  await userEvent.click(within(chat).getByRole("tab", { name: "Python" }))
  const python = within(chat).getByLabelText("Chat in Python")
  expect(python).toHaveTextContent('base_url=os.environ["MODEL_URL"] + "/v1"')
  expect(python).toHaveTextContent('api_key=os.environ["MODEL_API_KEY"]')
  expect(python).toHaveTextContent("from openai import OpenAI")
  await userEvent.click(within(chat).getByRole("tab", { name: "JavaScript" }))
  expect(within(chat).getByLabelText("Chat in JavaScript")).toHaveTextContent(
    'baseURL: process.env.MODEL_URL + "/v1"'
  )

  const embeddings = screen.getByRole("region", { name: "Embeddings" })
  expect(
    within(embeddings).getByLabelText("Embeddings in curl")
  ).toHaveTextContent("$MODEL_URL/v1/embeddings")
  expect(embeddings).toHaveTextContent("a prefix on each query")
  expect(
    within(embeddings).getByRole("link", { name: "open its card" })
  ).toHaveAttribute("href", "/ai/models/Qwen/Qwen2.5-7B-Instruct")

  expect(
    within(screen.getByRole("region", { name: "Reranking" })).getByLabelText(
      "Reranking in curl"
    )
  ).toHaveTextContent("$MODEL_URL/rerank")
  expect(
    within(
      screen.getByRole("region", { name: "Speech to text" })
    ).getByLabelText("Speech to text in curl")
  ).toHaveTextContent("$MODEL_URL/v1/audio/transcriptions")
  // The key itself is never on the page, only the variable.
  expect(document.body.textContent).not.toMatch(/Bearer [^$]/)
})

it("removes the model after asking, and says what stays", async () => {
  const stays =
    "The plan removes qwen2-5-7b-instruct. Its downloaded weights stay on gpu-box as retained storage, so deploying it there again doesn't download them again. Delete them from the server's storage when you no longer need them. Loomplane created checkout-qwen-gpu for this model, and it keeps running and costs money until you delete it in Infrastructure."
  let removed = false
  routes(modelStatus(), {
    [`DELETE ${modelPath}`]: () => {
      removed = true
      return Response.json({ plan_id: "plan_rm", message: stays })
    },
  })
  await openModel(`${page}&tab=settings`)
  const remove = screen.getByRole("region", { name: "Remove" })
  await userEvent.click(
    within(remove).getByRole("button", { name: "Remove qwen2-5-7b-instruct" })
  )
  const confirm = await screen.findByRole("alertdialog")
  expect(confirm).toHaveTextContent(
    "The model stops. The downloaded weights stay on gpu-box as retained storage."
  )
  expect(removed).toBe(false)
  await userEvent.click(
    within(confirm).getByRole("button", { name: "Remove qwen2-5-7b-instruct" })
  )
  const notice = (await screen.findByText(stays)).closest(
    "[role=status]"
  ) as HTMLElement
  expect(
    within(notice).getByRole("link", { name: "Open the plan" })
  ).toHaveAttribute("href", "/deployments/plan_rm")
  expect(
    within(notice).getByRole("link", { name: "Open Infrastructure" })
  ).toBeInTheDocument()
})

it("says which service still links to a model it won't remove", async () => {
  routes(modelStatus(), {
    [`DELETE ${modelPath}`]: () =>
      Response.json(
        {
          code: "in_use",
          message: "checkout in staging links to it. Unlink it first",
        },
        { status: 422 }
      ),
  })
  await openModel(`${page}&tab=settings`)
  const remove = screen.getByRole("region", { name: "Remove" })
  await userEvent.click(
    within(remove).getByRole("button", { name: "Remove qwen2-5-7b-instruct" })
  )
  await userEvent.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", {
      name: "Remove qwen2-5-7b-instruct",
    })
  )
  expect(
    await within(remove).findByText(
      "checkout in staging links to it. Unlink it first."
    )
  ).toBeInTheDocument()
})

type MoveBody = {
  target: { source: string; connection_id: string }
  review_server?: boolean
  server?: unknown
}

it("moves onto a new GPU server through its review, and says the old one still bills", async () => {
  const bills =
    "checkout-qwen-gpu keeps running and costs money until you delete it in Infrastructure."
  let n = 0
  const asked = routes(modelStatus(), {
    [`POST ${modelPath}/move-preview`]: async (_url, init) => {
      const body = JSON.parse(String(init?.body)) as MoveBody
      if (body.target.source !== "server_size")
        return Response.json(preview({ target: body.target }))
      if (!body.review_server)
        return Response.json(
          preview({
            target: body.target,
            target_name: "checkout-qwen-gpu-2",
            can_deploy: false,
            server_review_needed: true,
          })
        )
      n++
      return Response.json(
        preview({
          target: body.target,
          target_name: "checkout-qwen-gpu-2",
          server: operation({ id: `op_${n}`, review: `review_${n}` }),
        })
      )
    },
    [`POST ${modelPath}/move`]: () =>
      Response.json({
        deployment: { id: "aimodel_1", phase: "waiting_for_server" },
        message: bills,
      }),
  })
  await openModel(`${page}&tab=settings`)
  await userEvent.click(
    screen.getByRole("button", { name: "Move qwen2-5-7b-instruct" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.click(
    await within(dialog).findByRole("button", {
      name: "Choose gpu-l40sx1-48gb",
    })
  )
  const review = await within(dialog).findByRole("button", {
    name: "Review server",
  })
  await waitFor(() => expect(review).toBeEnabled())
  const plan = within(dialog).getByRole("button", {
    name: "Create the server and plan the move",
  })
  expect(plan).toBeDisabled()
  const moves = () =>
    asked.filter((a) => a.url.pathname.endsWith("/move-preview"))
  // Plain previews never review.
  expect(moves().every((a) => !(a.body as MoveBody).review_server)).toBe(true)
  // Two quick clicks review once.
  act(() => {
    review.click()
    review.click()
  })
  await within(dialog).findByTestId("server-review")
  expect(
    moves().filter((a) => (a.body as MoveBody).review_server)
  ).toHaveLength(1)
  await waitFor(() => expect(plan).toBeEnabled())
  await userEvent.click(plan)

  const notice = (await screen.findByText(bills)).closest(
    "[role=status]"
  ) as HTMLElement
  expect(notice).toHaveTextContent("waits for its new GPU server")
  expect(
    asked.find((a) => a.url.pathname.endsWith("/move"))?.body
  ).toMatchObject({
    target: { source: "server_size", connection_id: "conn_do" },
    server: { cloud_id: "cloud_1", operation_id: "op_1", review: "review_1" },
  })
})

it("keeps loading, denied, missing and failed apart", async () => {
  stub({ ...modelRoutes(), [modelPath]: () => new Promise(() => {}) })
  open(page)
  expect(await screen.findByTestId("model-loading")).toBeInTheDocument()

  fresh()
  stub({
    ...modelRoutes(),
    [modelPath]: () => Response.json({ message: "forbidden" }, { status: 403 }),
  })
  open(page)
  expect(await screen.findByText("Models are restricted")).toBeInTheDocument()

  fresh()
  stub({
    ...modelRoutes(),
    [modelPath]: () => Response.json({ message: "no" }, { status: 404 }),
  })
  open(page)
  expect(await screen.findByText("No such model here")).toBeInTheDocument()

  fresh()
  stub({
    ...modelRoutes(),
    [modelPath]: () => Response.json({ message: "" }, { status: 500 }),
  })
  open(page)
  expect(
    await screen.findByRole("button", { name: "Retry" })
  ).toBeInTheDocument()
})
