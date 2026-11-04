// What a workspace's pages sit in: the agent's status for this workspace,
// then the shell (command menu, dialogs, the agent's panel and the keys
// that open them). The shell reads the agent's status, so it goes inside.
import type { ReactNode } from "react"

import { ShellProvider } from "@/components/shell"
import { AgentProvider } from "@/lib/features"

export function WorkspaceFrame({
  orgId,
  children,
}: {
  orgId: string
  children: ReactNode
}) {
  return (
    // Keyed on the organization: the agent is on or off per workspace, and
    // the panel's conversation belongs to the workspace it was started in.
    <AgentProvider key={orgId}>
      <ShellProvider>{children}</ShellProvider>
    </AgentProvider>
  )
}
