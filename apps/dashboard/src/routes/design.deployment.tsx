import { createFileRoute } from "@tanstack/react-router"

import { DeploymentSetupPreview } from "@/mock/pages/deployment-setup"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"

export const Route = createFileRoute("/design/deployment")({
  // Keep this local design exercise out of production navigation.
  component: import.meta.env.DEV ? DeploymentSetupPreview : Unavailable,
})

function Unavailable() {
  return (
    <ZeroState>
      <ZeroStateHeader>
        <ZeroStateTitle>Development preview</ZeroStateTitle>
        <ZeroStateDescription>
          Run the development server to open this design preview.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}
