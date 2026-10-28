import { describe, expect, it } from "vitest"

import type { Cost, Fit } from "@/client/forge/loomplane/types"
import {
  costLabel,
  deployedSummary,
  formatContext,
  formatParams,
  gpuNeed,
  looksLikeToken,
  modelsSearch,
  monthLabel,
  registryQuery,
  repoOf,
  sizeBucket,
  verificationLabel,
} from "@/lib/models"

it("formats parameter counts", () => {
  expect(formatParams(8030261248)).toBe("8B")
  expect(formatParams(494032768)).toBe("494M")
  expect(formatParams(116829156672)).toBe("117B")
  expect(formatParams(1543714304)).toBe("1.5B")
  expect(formatParams(0)).toBe("Unknown size")
})

it("buckets sizes the way the server does", () => {
  expect(sizeBucket(3_999_999_999)).toBe("tiny")
  expect(sizeBucket(4_000_000_000)).toBe("small")
  expect(sizeBucket(15_000_000_000)).toBe("medium")
  expect(sizeBucket(70_553_706_496)).toBe("large")
  expect(sizeBucket(100_000_000_000)).toBe("large")
  expect(sizeBucket(116_829_156_672)).toBe("huge")
})

it("names context windows and what a model needs", () => {
  expect(formatContext(131072)).toBe("128K context")
  expect(formatContext(8192)).toBe("8K context")
  expect(formatContext(512)).toBe("512 context")
  expect(gpuNeed({ minGpuMemoryGib: 22, cpu: false })).toBe("Needs 22 GB GPU")
  expect(gpuNeed({ minGpuMemoryGib: 0, cpu: true })).toBe("Runs on CPU")
  expect(gpuNeed({ minGpuMemoryGib: 6, cpu: true })).toBe(
    "Needs 6 GB GPU, or runs on CPU"
  )
})

const need = { gpus: 1, maxModelLen: 32768, perGpuGib: 22 }

describe("verificationLabel", () => {
  it("says tested only for a qualification run", () => {
    const fit: Fit = {
      gpuKind: "nvidia-l40s",
      need,
      status: "tested",
      result: {
        detail: "",
        engine: "vllm",
        engineVersion: "0.31.0",
        entry: "meta-llama/Llama-3.1-8B-Instruct",
        gpuKind: "nvidia-l40s",
        gpus: 1,
        maxModelLen: 32768,
        outcome: "served",
        quantization: "bf16",
        ranAt: "2026-11-03T12:00:00Z",
        revision: "0".repeat(40),
        run: "1",
      },
    }
    expect(verificationLabel(fit, "tested")).toEqual({
      label: "Tested on NVIDIA L40S",
      tone: "overdye",
      detail:
        "Deployed and served by Loomplane's qualification on Nov 3, 2026 with vLLM 0.31.0.",
    })
  })

  it("never calls an estimate tested", () => {
    const fit: Fit = { gpuKind: "nvidia-l4", need, status: "estimated" }
    const v = verificationLabel(fit, "estimated")
    expect(v).toEqual({
      label: "Estimated",
      tone: "flight",
      detail: "Memory math only. Loomplane hasn't run this model on NVIDIA L4.",
    })
    expect(`${v.label} ${v.detail}`).not.toMatch(/tested/i)
  })

  it("says what failed, and that a pasted model is unchecked", () => {
    const failed = verificationLabel(
      {
        gpuKind: "nvidia-l40s",
        need,
        status: "failed",
        result: {
          detail: "The engine ran out of memory loading the weights.",
        } as Fit["result"],
      },
      "failed"
    )
    expect(failed.label).toBe("Failed on NVIDIA L40S")
    expect(failed.tone).toBe("failed")
    expect(failed.detail).toBe(
      "The engine ran out of memory loading the weights."
    )
    expect(verificationLabel(undefined, "untested")).toEqual({
      label: "Untested",
      tone: "weld",
      detail: "You pasted this from Hugging Face. Loomplane hasn't checked it.",
    })
  })
})

function cost(over: Partial<Cost>): Cost {
  return {
    estimate: false,
    hourlyUsd: "",
    known: false,
    monthlyUsd: "",
    share: "",
    source: "unknown",
    ...over,
  }
}

describe("costs", () => {
  it("never shows a CPU model or an unknown price as $0", () => {
    const cpu = cost({ source: "cpu", known: true, hourlyUsd: "0.00" })
    const label = costLabel(cpu, "build-box")
    expect(label.detail).toBe("Runs on CPU. Its cost is part of build-box.")
    expect(`${label.hourly} ${label.detail}`).not.toContain("$0")
    expect(monthLabel({ cost: cpu, monthToDateUsd: "0.0000" })).toBe(
      "Part of the server"
    )
    const unknown = cost({})
    expect(costLabel(unknown).hourly).toBe("unknown")
    expect(monthLabel({ cost: unknown, monthToDateUsd: "0.0000" })).toBe(
      "unknown"
    )
  })

  it("marks an estimate", () => {
    const label = costLabel(
      cost({
        source: "cluster",
        known: true,
        estimate: true,
        hourlyUsd: "0.8150",
        share: "1 of 4 GPUs",
      })
    )
    expect(label.hourly).toBe("~$0.82/hr")
    expect(label.kind).toBe("estimate")
  })

  it("sums GPU models and counts unknown prices apart", () => {
    const models = [
      { cost: cost({ source: "server", known: true, hourlyUsd: "1.2000" }) },
      { cost: cost({ source: "override", known: true, hourlyUsd: "0.5000" }) },
      { cost: cost({}) },
      { cost: cost({ source: "cpu", known: true, hourlyUsd: "0.00" }) },
    ]
    expect(deployedSummary(models)).toBe(
      "GPU models cost about $1.70/hr together, plus 1 with an unknown price."
    )
    expect(deployedSummary([{ cost: cost({}) }, { cost: cost({}) }])).toBe(
      "2 GPU models have an unknown price."
    )
    expect(
      deployedSummary([
        { cost: cost({ source: "cpu", known: true, hourlyUsd: "0.00" }) },
      ])
    ).toBe("")
  })
})

describe("the URL", () => {
  it("keeps the filters it knows and drops the rest", () => {
    expect(
      modelsSearch({
        tab: "deployed",
        q: "llama",
        task: "code",
        size: "small",
        license: "apache-2.0",
        commercial: true,
        fits: "true",
        tested: "yes",
        source: "external",
      })
    ).toEqual({
      tab: "deployed",
      q: "llama",
      task: "code",
      size: "small",
      license: "apache-2.0",
      commercial: true,
      fits: true,
    })
    expect(modelsSearch({ task: "painting", size: "enormous" })).toEqual({})
    expect(
      registryQuery({ tab: "deployed", task: "chat", fits: true })
    ).toEqual({ task: "chat", fits: true })
  })

  it("reads a pasted repo and catches a pasted token", () => {
    expect(repoOf(" Qwen/Qwen2.5-7B-Instruct ")).toBe(
      "Qwen/Qwen2.5-7B-Instruct"
    )
    expect(
      repoOf("https://huggingface.co/Qwen/Qwen2.5-7B-Instruct/tree/main")
    ).toBe("Qwen/Qwen2.5-7B-Instruct")
    expect(repoOf("not a repo")).toBeUndefined()
    expect(looksLikeToken("hf_abcdefghijklmnop")).toBe(true)
    expect(looksLikeToken("Qwen/Qwen2.5-7B-Instruct")).toBe(false)
  })
})
