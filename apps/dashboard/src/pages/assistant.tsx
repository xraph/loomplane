// The Assistant's old address. The agent lives in a panel that's part of
// the layout now, so a bookmark or a link here opens that panel at full
// width (on the conversation it names, or a new one about the project it
// names) and leaves for the Overview underneath it.
import { useEffect, useRef } from "react"

import { Page } from "@/components/page"
import { PageNotFound } from "@/components/route-feedback"
import { useShell } from "@/components/shell"
import { Skeleton } from "@/components/ui/skeleton"
import { useAssistantAvailable } from "@/lib/agent-stream"
import { useAgent } from "@/lib/features"
import { useNavigate } from "@/lib/router"

export function Assistant({
  conversationId,
  projectId,
}: {
  conversationId?: string
  /** The project a new conversation is about. */
  projectId?: string
}) {
  const available = useAssistantAvailable()
  const agent = useAgent()
  if (!available) {
    if (agent.loading)
      return (
        <Page>
          <Skeleton className="h-[60svh] w-full rounded-[14px]" />
        </Page>
      )
    return <PageNotFound />
  }
  return <OpenPanel conversationId={conversationId} projectId={projectId} />
}

function OpenPanel({
  conversationId,
  projectId,
}: {
  conversationId?: string
  projectId?: string
}) {
  const { openAssistant } = useShell()
  const navigate = useNavigate()
  const done = useRef(false)
  useEffect(() => {
    if (done.current) return
    done.current = true
    openAssistant({ conversationId, projectId, mode: "focus" })
    void navigate({ to: "/", replace: true })
  }, [openAssistant, navigate, conversationId, projectId])
  return null
}
