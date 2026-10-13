// A cloud's small monochrome tile, the way the mock draws providers.
import { cloudLabel } from "@/lib/clouds"
import { cn } from "@/lib/utils"

export function CloudGlyph({
  cloud,
  className,
}: {
  cloud: string
  className?: string
}) {
  const label = cloudLabel(cloud)
  return (
    <span
      title={label}
      className={cn(
        "inline-flex size-5 shrink-0 items-center justify-center rounded-[5px] border bg-background text-[9px] leading-none font-semibold tracking-tight text-foreground",
        className
      )}
    >
      {cloud === "vercel" ? (
        <svg viewBox="0 0 10 10" className="size-2.5" aria-hidden="true">
          <path d="M5 1.2 9.2 8.6H.8Z" className="fill-current" />
        </svg>
      ) : cloud === "aws" ? (
        "aws"
      ) : cloud === "gcp" ? (
        "G"
      ) : (
        (label[0]?.toUpperCase() ?? "?")
      )}
      <span className="sr-only">{label}</span>
    </span>
  )
}
