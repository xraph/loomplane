import { CloudIcon } from "lucide-react"
import type { ReactNode } from "react"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"

export function NativeEmpty({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  return (
    <ZeroState variant="inline" className="p-3">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <CloudIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{children}</ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}
