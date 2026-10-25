import { LayersIcon } from "lucide-react"
import type { ComponentProps } from "react"

import { cn } from "@/lib/utils"

export function FabriqIcon({
  className,
  ...props
}: ComponentProps<typeof LayersIcon>) {
  return <LayersIcon {...props} className={cn("text-flight-ink", className)} />
}
