// A copy of the accelerator kind table in pkg/provider/accelerator.go.
// Kinds the table doesn't know show as their own ID.
const gpuTitles: Record<string, string> = {
  "amd-mi300x": "AMD Instinct MI300X",
  "nvidia-a100-40gb": "NVIDIA A100 40GB",
  "nvidia-a100-80gb": "NVIDIA A100 80GB",
  "nvidia-a10": "NVIDIA A10",
  "nvidia-a10g": "NVIDIA A10G",
  "nvidia-h100-80gb": "NVIDIA H100 80GB",
  "nvidia-h100-nvl-94gb": "NVIDIA H100 NVL",
  "nvidia-h200-141gb": "NVIDIA H200",
  "nvidia-l40s": "NVIDIA L40S",
  "nvidia-l4": "NVIDIA L4",
  "nvidia-rtx-4000-ada": "NVIDIA RTX 4000 Ada",
  "nvidia-rtx-6000-ada": "NVIDIA RTX 6000 Ada",
  "nvidia-t4": "NVIDIA T4",
  "nvidia-v100-16gb": "NVIDIA V100 16GB",
  "nvidia-v100-32gb": "NVIDIA V100 32GB",
}

/** nvidiaKinds are the kinds a GPU ceiling can name: the table's NVIDIA ones. */
export const nvidiaKinds = Object.keys(gpuTitles).filter((k) =>
  k.startsWith("nvidia-")
)

export const gpuDocsUrl = "https://loomplane.dev/docs/clouds/gpus"

const hoursPerMonth = 730

export function gpuTitle(kind: string): string {
  return Object.hasOwn(gpuTitles, kind) ? gpuTitles[kind] : kind
}

export function formatHourly(usd: number, known: boolean): string {
  return known ? `$${usd.toFixed(2)}/hr` : "price unknown"
}

export function formatMonthly(usd: number): string {
  const monthly = Math.round(usd * hoursPerMonth)
  return `about $${monthly.toLocaleString("en-US")}/mo`
}

// A price Loomplane doesn't know reads as unknown, never as $0.
export function priceKnown(
  usd: number | undefined,
  known: boolean | undefined
): usd is number {
  return known === true && typeof usd === "number"
}

export function hourlyOrUnknown(
  usd: number | undefined,
  known: boolean | undefined
): string {
  return priceKnown(usd, known)
    ? formatHourly(usd, true)
    : formatHourly(0, false)
}

// "1× NVIDIA H100 80GB": how many GPUs of a kind one server or node has.
export function acceleratorLabel(a: { count: number; kind: string }): string {
  return `${a.count}× ${gpuTitle(a.kind)}`
}

type GpuServer = {
  status?: string
  size: string
  accelerator?: { count: number; kind: string }
  hourlyUsd?: number
  hourlyPriceKnown?: boolean
  pricedSize?: string
  health: {
    gpuRuntime?: string
    gpus?: { kind: string }[]
  }
}

// The GPUs a server has: its devices once a check listed them, else what its
// size bought. Several kinds show as one label each, joined.
export function serverGpuLabel(s: GpuServer): string {
  const devices = s.health.gpus ?? []
  if (!devices.length)
    return s.accelerator ? acceleratorLabel(s.accelerator) : ""
  const counts = new Map<string, number>()
  for (const d of devices) counts.set(d.kind, (counts.get(d.kind) ?? 0) + 1)
  return [...counts]
    .map(([kind, count]) =>
      kind
        ? acceleratorLabel({ count, kind })
        : `${count}× GPU, kind not labelled`
    )
    .join(", ")
}

// A server's hourly price, unless it was priced for a size it no longer has.
export function serverHourly(s: GpuServer): number | undefined {
  if (!priceKnown(s.hourlyUsd, s.hourlyPriceKnown)) return undefined
  if (s.pricedSize && s.pricedSize !== s.size) return undefined
  return s.hourlyUsd
}

// How many GPUs of kind a server's last check listed.
export function serverGpusOfKind(s: GpuServer, kind: string): number {
  return (s.health.gpus ?? []).filter((d) => d.kind === kind).length
}

// Whether a ready server looks like it has GPUs Loomplane couldn't count:
// its devices are of mixed or missing kinds, or a check reported a GPU
// status without listing any and its size names no GPUs. A server that was
// never asked about GPUs, on a size without them, is a CPU server. The
// capacity ledger holds back GPU growth while it can't count a server.
export function gpusUnread(s: GpuServer): boolean {
  if (s.status !== "ready") return false
  const devices = s.health.gpus ?? []
  if (devices.length)
    return devices.some((d) => !d.kind || d.kind !== devices[0]!.kind)
  const runtime = s.health.gpuRuntime ?? ""
  if (runtime === "" || runtime === "none") return false
  return !s.accelerator
}
