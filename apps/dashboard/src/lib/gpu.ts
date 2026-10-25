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
