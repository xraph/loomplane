import type { ReactNode } from "react"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
} from "@/components/zero-state"

export function FabriqEmpty({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children?: ReactNode
}) {
  return (
    <ZeroState>
      <ZeroStateHeader>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      {children && <ZeroStateContent>{children}</ZeroStateContent>}
    </ZeroState>
  )
}
