// How sure Loomplane is that a model runs where it says: tested by a
// qualification run, estimated from the memory math, failed, or pasted
// from Hugging Face and unchecked. An estimate never reads as tested.
import type { Fit } from "@/client/forge/loomplane/types"
import { StatusPill, type Tone } from "@/components/runs"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { verificationLabel, type VerificationTone } from "@/lib/models"

// Status tones, never the accent.
const tones: Record<VerificationTone, Tone> = {
  overdye: "live",
  flight: "flight",
  weld: "waiting",
  failed: "failed",
}

export function VerificationBadge({
  status,
  fit,
  gpuTitle,
}: {
  status: string
  fit?: Fit
  gpuTitle?: string
}) {
  const v = verificationLabel(fit, status, gpuTitle)
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={`${v.label}. ${v.detail}`}
            className="w-fit max-w-full rounded-full outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          />
        }
      >
        <StatusPill tone={tones[v.tone]} className="max-w-full">
          <span className="truncate">{v.label}</span>
        </StatusPill>
      </TooltipTrigger>
      <TooltipContent>{v.detail}</TooltipContent>
    </Tooltip>
  )
}
