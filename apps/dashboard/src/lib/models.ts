// What the Models page shows about a model: its size, its context, how sure
// Loomplane is that it runs where it says, and what it costs.
import type {
  Cost,
  EntrySummary,
  Fit,
  ModelsStatus,
} from "@/client/forge/loomplane/types"
import { gpuTitle } from "@/lib/gpu"

/** The tasks the registry serves, as its filter chips name them. */
export const modelTasks = [
  { value: "chat", label: "Chat" },
  { value: "reasoning", label: "Reasoning" },
  { value: "code", label: "Code" },
  { value: "vision", label: "Vision" },
  { value: "embedding", label: "Embeddings" },
  { value: "rerank", label: "Reranking" },
  { value: "speech-to-text", label: "Speech to text" },
] as const

export type ModelTask = (typeof modelTasks)[number]["value"]

/** taskLabel names a task the registry serves, or shows its ID. */
export function taskLabel(task: string): string {
  return modelTasks.find((t) => t.value === task)?.label ?? task
}

/** The size buckets, by parameter count, as the server buckets them. */
export const modelSizes = [
  { value: "tiny", label: "Under 4B" },
  { value: "small", label: "4B to 15B" },
  { value: "medium", label: "15B to 40B" },
  { value: "large", label: "40B to 100B" },
  { value: "huge", label: "Over 100B" },
] as const

export type ModelSize = (typeof modelSizes)[number]["value"]

const billion = 1_000_000_000

/** sizeBucket is the bucket a parameter count falls in. */
export function sizeBucket(n: number): ModelSize {
  if (n < 4 * billion) return "tiny"
  if (n < 15 * billion) return "small"
  if (n < 40 * billion) return "medium"
  if (n <= 100 * billion) return "large"
  return "huge"
}

/**
 * formatParams is a parameter count the way model names say it:
 * 8030261248 is "8B", 494032768 is "494M", 1543714304 is "1.5B".
 */
export function formatParams(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "Unknown size"
  const units: [number, string][] = [
    [1e12, "T"],
    [1e9, "B"],
    [1e6, "M"],
    [1e3, "K"],
  ]
  for (const [size, unit] of units) {
    if (n >= size) {
      const v = n / size
      // One decimal under 10, so 1.5B and 3.8B keep their names.
      const shown = v < 10 ? Math.round(v * 10) / 10 : Math.round(v)
      return `${shown}${unit}`
    }
  }
  return String(n)
}

/** formatContext is a context window: 131072 is "128K context". */
export function formatContext(tokens: number): string {
  if (!tokens || tokens <= 0) return ""
  if (tokens >= 1024 * 1024 && tokens % (1024 * 1024) === 0)
    return `${tokens / (1024 * 1024)}M context`
  if (tokens >= 1024 && tokens % 1024 === 0) return `${tokens / 1024}K context`
  return `${tokens.toLocaleString("en-US")} context`
}

/** The tones a verification badge takes: status colors, never the accent. */
export type VerificationTone = "overdye" | "flight" | "weld" | "failed"

export type VerificationLabel = {
  label: string
  tone: VerificationTone
  detail: string
}

const engineNames: Record<string, string> = {
  vllm: "vLLM",
  tei: "TEI",
  llamacpp: "llama.cpp",
  "llamacpp-gpu": "llama.cpp",
}

const ranAtFormat = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  year: "numeric",
})

function where(kind: string | undefined): string {
  if (!kind) return ""
  return kind === "cpu" ? "CPU" : gpuTitle(kind)
}

/**
 * verificationLabel says how sure Loomplane is that a model runs: tested
 * only when a qualification run deployed and served it, never for an
 * estimate. fit is one configuration's fit when the page has it; gpu names
 * where it was tested when it doesn't, such as on a registry card.
 */
export function verificationLabel(
  fit: Fit | undefined,
  status: string,
  gpu?: string
): VerificationLabel {
  const on = where(fit?.gpuKind) || gpu || ""
  switch (status) {
    case "tested": {
      const r = fit?.result
      const ran = r?.ranAt ? new Date(r.ranAt) : undefined
      const when =
        ran && !Number.isNaN(ran.getTime())
          ? ` on ${ranAtFormat.format(ran)}`
          : ""
      const engine = r?.engine
        ? ` with ${engineNames[r.engine] ?? r.engine}${r.engineVersion ? ` ${r.engineVersion}` : ""}`
        : ""
      return {
        label: on ? `Tested on ${on}` : "Tested",
        tone: "overdye",
        detail: `Deployed and served by Loomplane's qualification${when}${engine}.`,
      }
    }
    case "failed":
      return {
        label: on ? `Failed on ${on}` : "Failed",
        tone: "failed",
        detail:
          fit?.result?.detail ||
          fit?.reason ||
          "Loomplane's qualification couldn't serve it.",
      }
    case "untested":
      return {
        label: "Untested",
        tone: "weld",
        detail:
          "You pasted this from Hugging Face. Loomplane hasn't checked it.",
      }
    case "no-fit":
      return {
        label: on ? `Doesn't fit ${on}` : "Doesn't fit",
        tone: "failed",
        detail: fit?.reason || "The memory math says it doesn't fit.",
      }
    default:
      return {
        label: "Estimated",
        tone: "flight",
        detail: on
          ? `Memory math only. Loomplane hasn't run this model on ${on}.`
          : "Memory math only. Loomplane hasn't run this model yet.",
      }
  }
}

/** gpuNeed is what a card says a model needs to run. */
export function gpuNeed(
  entry: Pick<EntrySummary, "minGpuMemoryGib" | "cpu">
): string {
  const gpu = entry.minGpuMemoryGib > 0
  if (gpu && entry.cpu)
    return `Needs ${entry.minGpuMemoryGib} GB GPU, or runs on CPU`
  if (gpu) return `Needs ${entry.minGpuMemoryGib} GB GPU`
  return entry.cpu ? "Runs on CPU" : ""
}

/** The registry's filters, as the page's URL holds them. */
export type ModelsSearch = {
  tab?: "deployed"
  q?: string
  task?: ModelTask
  size?: ModelSize
  license?: string
  commercial?: true
  fits?: true
  tested?: true
}

/**
 * modelsSearch reads the page's URL. Anything it doesn't know is dropped,
 * so a stale link shows the whole registry rather than an empty one.
 */
export function modelsSearch(search: Record<string, unknown>): ModelsSearch {
  const out: ModelsSearch = {}
  if (search.tab === "deployed") out.tab = "deployed"
  if (typeof search.q === "string" && search.q.trim() && search.q.length <= 100)
    out.q = search.q
  const task = modelTasks.find((t) => t.value === search.task)
  if (task) out.task = task.value
  const size = modelSizes.find((s) => s.value === search.size)
  if (size) out.size = size.value
  if (
    typeof search.license === "string" &&
    /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(search.license)
  )
    out.license = search.license
  for (const flag of ["commercial", "fits", "tested"] as const) {
    if (search[flag] === true || search[flag] === "true") out[flag] = true
  }
  return out
}

/** registryQuery is what the registry API is asked for a page's filters. */
export function registryQuery(
  s: ModelsSearch
): Record<string, string | boolean> {
  const q: Record<string, string | boolean> = {}
  if (s.q?.trim()) q.q = s.q.trim()
  if (s.task) q.task = s.task
  if (s.size) q.size = s.size
  if (s.license) q.license = s.license
  if (s.commercial) q.commercial = true
  if (s.fits) q.fits = true
  if (s.tested) q.tested = true
  return q
}

const usd = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

function amount(text: string | undefined): number | undefined {
  if (!text) return undefined
  const n = Number(text)
  return Number.isFinite(n) ? n : undefined
}

/**
 * The cost of a deployed model, the way a row says it. A CPU model's cost
 * is part of its server's, so it never reads as $0.00, and a price nobody
 * knows reads as unknown.
 */
export type CostLabel = {
  /** Short, for a column: "$1.20/hr", "~$0.82/hr", "Runs on CPU", "unknown". */
  hourly: string
  /** The sentence behind it, for a tooltip or the row on a phone. */
  detail: string
  kind: "known" | "estimate" | "cpu" | "unknown"
}

export function costLabel(cost: Cost, server?: string): CostLabel {
  if (cost.source === "cpu")
    return {
      hourly: "Runs on CPU",
      detail: `Runs on CPU. Its cost is part of ${server || "the server"}.`,
      kind: "cpu",
    }
  const hourly = amount(cost.hourlyUsd)
  if (!cost.known || hourly === undefined)
    return {
      hourly: "unknown",
      detail:
        "Loomplane doesn't know what this GPU costs, so it isn't in the total.",
      kind: "unknown",
    }
  const share = cost.share ? ` for ${cost.share}` : ""
  if (cost.estimate)
    return {
      hourly: `~${usd.format(hourly)}/hr`,
      detail: `An estimate from the node's list price${share}.`,
      kind: "estimate",
    }
  return {
    hourly: `${usd.format(hourly)}/hr`,
    detail:
      cost.source === "override"
        ? "The price you set."
        : `The server's price${share}.`,
    kind: "known",
  }
}

/** monthLabel is what a deployed model cost this month, by its cost. */
export function monthLabel(
  status: Pick<ModelsStatus, "cost" | "monthToDateUsd">
): string {
  if (status.cost.source === "cpu") return "Part of the server"
  const spent = amount(status.monthToDateUsd)
  if (!status.cost.known || spent === undefined) return "unknown"
  return usd.format(spent)
}

/**
 * deployedSummary is the line above the Deployed tab: what its GPU models
 * cost per hour together, with the ones whose price nobody knows counted
 * apart, never as zero. CPU models are part of their servers, so they're
 * left out. Empty when there are no GPU models.
 */
export function deployedSummary(models: Pick<ModelsStatus, "cost">[]): string {
  let total = 0
  let known = 0
  let unknown = 0
  let estimated = false
  for (const m of models) {
    if (m.cost.source === "cpu") continue
    const hourly = amount(m.cost.hourlyUsd)
    if (!m.cost.known || hourly === undefined) {
      unknown++
      continue
    }
    known++
    total += hourly
    if (m.cost.estimate) estimated = true
  }
  if (known === 0 && unknown === 0) return ""
  if (known === 0)
    return unknown === 1
      ? "1 GPU model has an unknown price."
      : `${unknown} GPU models have an unknown price.`
  const unknownPart = unknown ? `, plus ${unknown} with an unknown price` : ""
  const estimatePart = estimated ? " Some are estimates from list prices." : ""
  return `GPU models cost about ${usd.format(total)}/hr together${unknownPart}.${estimatePart}`
}

/**
 * repoOf reads a Hugging Face repo out of what someone pasted: the repo
 * itself or its page's address. undefined when it isn't one.
 */
export function repoOf(input: string): string | undefined {
  let text = input.trim()
  const url = /^https?:\/\/(www\.)?huggingface\.co\/(.+)$/i.exec(text)
  if (url) text = url[2].split(/[?#]/)[0].split("/").slice(0, 2).join("/")
  return /^[A-Za-z0-9][\w.-]{0,95}\/[A-Za-z0-9][\w.-]{0,95}$/.test(text)
    ? text
    : undefined
}

/**
 * looksLikeToken catches a Hugging Face token pasted where a repo goes, so
 * it's never sent to the server, where a refusal could repeat it.
 */
export function looksLikeToken(input: string): boolean {
  return /^\s*hf_[A-Za-z0-9]{8,}\s*$/.test(input)
}
