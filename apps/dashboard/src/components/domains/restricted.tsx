// What someone without Cloud access sees on a Domains page.
import { LockIcon } from "lucide-react"

import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"

export function Restricted() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>Domain access is restricted</ZeroStateTitle>
        <ZeroStateDescription>
          Ask your workspace owner for permission to manage Cloud
          infrastructure.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}
