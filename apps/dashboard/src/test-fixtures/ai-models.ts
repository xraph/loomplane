// What the AI models API answers in page tests, in its own snake_case.
import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { render } from "@testing-library/react"
import { createElement } from "react"
import { vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { createDashboardRouter } from "@/router"

export const qwen = "Qwen/Qwen2.5-7B-Instruct"

export const license = {
  id: "apache-2.0",
  title: "Apache 2.0",
  url: "https://www.apache.org/licenses/LICENSE-2.0",
  commercial: true,
}

export function summary(over: Record<string, unknown> = {}) {
  return {
    id: qwen,
    title: "Qwen2.5 7B Instruct",
    publisher: "Qwen",
    summary: "A chat model that also writes code.",
    family: "qwen",
    tasks: ["chat", "code"],
    parameters: 7615616512,
    context_length: 131072,
    license,
    gated: false,
    verification: "tested",
    tested_on: ["NVIDIA L40S"],
    min_gpu_memory_gib: 18,
    cpu: true,
    ...over,
  }
}

const tested = {
  detail: "first token 210 ms, 61 tokens/s",
  engine: "vllm",
  engine_version: "0.31.0",
  entry: qwen,
  gpu_kind: "nvidia-l40s",
  gpus: 1,
  max_model_len: 32768,
  outcome: "passed",
  quantization: "bf16",
  ran_at: "2026-11-03T10:00:00Z",
  revision: "a".repeat(40),
  run: "https://github.com/xraph/loomplane/actions/runs/1",
}

/**
 * detail is the entry with two quantizations: bf16 on vLLM, tested on an
 * L40S, estimated on an L4, too big for a T4 and failed on an A10; and a
 * llama.cpp GGUF with no min_gpu_memory_gib, whose one-GPU need is 6 GB.
 */
export function detail(over: Record<string, unknown> = {}) {
  return {
    ...summary(),
    revision: "a".repeat(40),
    hugging_face_url: `https://huggingface.co/${qwen}`,
    quantizations: [
      {
        id: "bf16",
        engine: "vllm",
        weight_bytes: 15231233024,
        min_gpu_memory_gib: 18,
        fits: [
          {
            gpu_kind: "nvidia-l40s",
            need: { gpus: 1, max_model_len: 32768, per_gpu_gib: 18 },
            status: "tested",
            result: tested,
          },
          {
            gpu_kind: "nvidia-l4",
            need: { gpus: 1, max_model_len: 8192, per_gpu_gib: 18 },
            status: "estimated",
          },
          {
            gpu_kind: "nvidia-t4",
            need: { gpus: 0, max_model_len: 0, per_gpu_gib: 18 },
            status: "no-fit",
            reason: "It needs 18 GB on one GPU, and a T4 has 16 GB.",
          },
          {
            gpu_kind: "nvidia-a10",
            need: { gpus: 1, max_model_len: 32768, per_gpu_gib: 18 },
            status: "failed",
            result: {
              ...tested,
              gpu_kind: "nvidia-a10",
              outcome: "failed",
              detail: "CUDA out of memory at 32768 tokens.",
            },
            reason: "CUDA out of memory at 32768 tokens.",
          },
        ],
      },
      {
        id: "gguf-q4_k_m",
        engine: "llamacpp",
        weight_bytes: 4683073536,
        file: "qwen2.5-7b-instruct-q4_k_m.gguf",
        repo: "Qwen/Qwen2.5-7B-Instruct-GGUF",
        revision: "b".repeat(40),
        fits: [
          {
            gpu_kind: "nvidia-l4",
            need: { gpus: 1, max_model_len: 32768, per_gpu_gib: 6 },
            status: "estimated",
          },
          {
            gpu_kind: "cpu",
            need: { gpus: 0, max_model_len: 32768, per_gpu_gib: 0, ram_gib: 8 },
            status: "estimated",
          },
        ],
      },
    ],
    ...over,
  }
}

export const projects = {
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
}

function environment(id: string, name: string, kind: string) {
  return {
    id,
    name,
    kind,
    org_id: "org",
    project_id: "proj_1",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  }
}

export const environments = {
  environments: [
    environment("env_stg", "staging", "staging"),
    environment("env_prod", "production", "production"),
  ],
}

function target(over: Record<string, unknown>) {
  return {
    source: "connection",
    cloud: "server",
    free: 1,
    fits: true,
    hourly_usd: 1.57,
    hourly_price_known: true,
    price_estimate: false,
    ...over,
  }
}

export const gpuTargets = {
  targets: [
    target({
      connection_id: "conn_gpu",
      name: "gpu-box",
      accelerator: {
        kind: "nvidia-l40s",
        count: 1,
        memory_gib: 48,
        vendor: "nvidia",
      },
    }),
    target({
      connection_id: "conn_a10",
      name: "a10-box",
      accelerator: {
        kind: "nvidia-a10",
        count: 1,
        memory_gib: 24,
        vendor: "nvidia",
      },
      hourly_usd: 0.76,
    }),
    target({
      connection_id: "conn_t4",
      name: "t4-box",
      accelerator: {
        kind: "nvidia-t4",
        count: 1,
        memory_gib: 16,
        vendor: "nvidia",
      },
      fits: false,
      message: "Its T4 has 16 GB, and this needs 18 GB.",
      hourly_usd: 0.35,
    }),
    target({
      source: "server_size",
      connection_id: "conn_do",
      cloud: "digitalocean",
      name: "gpu-l40sx1-48gb",
      size_id: "gpu-l40sx1-48gb",
      regions: ["tor1", "nyc2"],
      accelerator: {
        kind: "nvidia-l40s",
        count: 1,
        memory_gib: 48,
        vendor: "nvidia",
      },
    }),
  ],
  unreachable: [],
}

function connection(id: string, name: string, cloud: string, status: string) {
  return {
    id,
    name,
    cloud,
    status,
    account: "",
    created_at: "2026-10-01T00:00:00Z",
    created_by: "u",
    details: {},
    generation: 1,
    message: "",
    method: "",
    missing: [],
    org_id: "org",
    updated_at: "2026-10-01T00:00:00Z",
  }
}

export const connections = {
  connections: [
    connection("conn_gpu", "gpu-box", "server", "ready"),
    connection("conn_cpu", "build-box", "server", "ready"),
    connection("conn_k8s", "edge-cluster", "kubernetes", "failed"),
    connection("conn_do", "DigitalOcean", "digitalocean", "ready"),
  ],
}

export const reach = {
  environment: "staging",
  environment_id: "env_stg",
  mode: "home",
  message:
    "Runs in staging. Services there link to it and get MODEL_URL and MODEL_API_KEY.",
}

export const knownCost = {
  estimate: false,
  hourly_usd: "1.5700",
  known: true,
  monthly_usd: "1146.10",
  share: "1 of 1 GPU",
  source: "server",
}

/** preview is a deploy preview of bf16 on gpu-box that can deploy. */
export function preview(over: Record<string, unknown> = {}) {
  return {
    can_deploy: true,
    cost: knownCost,
    entry: summary(),
    environments: [reach],
    fit: {
      gpu_kind: "nvidia-l40s",
      need: { gpus: 1, max_model_len: 32768, per_gpu_gib: 18 },
      status: "tested",
      result: tested,
    },
    gated: false,
    gpu: { kind: "nvidia-l40s", count: 1 },
    kind: "model-vllm",
    max_model_len: 32768,
    problems: [],
    quantization: "bf16",
    resource: "qwen2-5-7b-instruct",
    target: { source: "connection", connection_id: "conn_gpu" },
    target_name: "gpu-box",
    token_ready: false,
    version: "0.31",
    ...over,
  }
}

/** operation is the review of the GPU server a server size creates. */
export function operation(over: Record<string, unknown> = {}) {
  return {
    id: "op_1",
    cloud_id: "cloud_1",
    review: "review_1",
    status: "preview",
    org_id: "org",
    account_id: "acct",
    actions: [],
    attempt: 0,
    attempted: false,
    bootstrap_expires_at: "2026-11-03T10:15:00Z",
    connection_id: "conn_do",
    connection_revision: "1",
    created_at: "2026-11-03T10:00:00Z",
    enrolled: false,
    estimated_hourly_usd: 1.57,
    hourly_price_known: true,
    expires_at: "2026-11-03T10:15:00Z",
    input: {
      kind: "create",
      name: "checkout-qwen2-5-7b-instruct-gpu",
      region: "tor1",
      size: "gpu-l40sx1-48gb",
      cloud_revision: 3,
    },
    message: "",
    ownership: "loomplane",
    provider: "digitalocean",
    public_ipv4: [],
    resource_id: "",
    server_id: "",
    state_revision: 1,
    updated_at: "2026-11-03T10:00:00Z",
    accelerator: {
      kind: "nvidia-l40s",
      count: 1,
      memory_gib: 48,
      vendor: "nvidia",
    },
    ...over,
  }
}

export type Handler = (
  url: URL,
  init?: RequestInit
) => Response | Promise<Response>

/**
 * stub answers fetch from routes keyed "METHOD path" or just "path" for
 * GET, and records every request with its JSON body.
 */
export function stub(routes: Record<string, Handler>) {
  const asked: { method: string; url: URL; body?: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = (init?.method ?? "GET").toUpperCase()
      const body =
        typeof init?.body === "string" ? JSON.parse(init.body) : undefined
      asked.push({ method, url, body })
      const route =
        routes[`${method} ${url.pathname}`] ??
        (method === "GET" ? routes[url.pathname] : undefined)
      if (route) return route(url, init)
      return Response.json(
        { message: `unexpected ${method} ${url.pathname}` },
        { status: 404 }
      )
    }
  )
  return asked
}

/** open renders the dashboard's router at path. */
export function open(path: string) {
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: [path] })
  )
  render(
    createElement(
      ClientProvider,
      { client: forgeClient },
      createElement(RouterProvider, { router })
    )
  )
  return router
}

/** The routes every deploy dialog test needs, besides preview and deploy. */
export function dialogRoutes(): Record<string, Handler> {
  return {
    "/api/v1/ai/models/registry/entry": () => Response.json(detail()),
    "/api/v1/ai/models/registry/card": () =>
      Response.json({
        markdown: "# Qwen2.5",
        source_url: `https://huggingface.co/${qwen}`,
      }),
    "/api/v1/projects": () => Response.json(projects),
    "/api/v1/projects/proj_1/environments": () => Response.json(environments),
    "/api/v1/gpu-targets": () => Response.json(gpuTargets),
    "/api/v1/connections": () => Response.json(connections),
    "/api/v1/ai/models/deployments": () => Response.json({ models: [] }),
  }
}

export const previewPath =
  "POST /api/v1/projects/proj_1/environments/env_stg/ai/models/preview"
export const deployPath =
  "POST /api/v1/projects/proj_1/environments/env_stg/ai/models"
