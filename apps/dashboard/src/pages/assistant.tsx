// The Assistant: talk to the workspace's agent. Your conversations on the
// left (a select on a phone), the open one on the right.
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { ConversationList } from "@/components/assistant/conversation-list"
import { ConversationView } from "@/components/assistant/conversation-view"
import { Page, PageHeading } from "@/components/page"
import { PageNotFound } from "@/components/route-feedback"
import { SiteHeader } from "@/components/site-header"
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
  return <AssistantPage conversationId={conversationId} projectId={projectId} />
}

function AssistantPage({
  conversationId,
  projectId,
}: {
  conversationId?: string
  projectId?: string
}) {
  const navigate = useNavigate()
  const projects = useQuery(useProjectsList)
  const project = projectId
    ? projects.data?.projects.find((p) => p?.id === projectId)
    : undefined
  const open = (id: string | undefined, replace = false) =>
    void navigate({
      to: "/assistant",
      search: id ? { c: id } : projectId ? { project: projectId } : {},
      replace,
    })

  return (
    <>
      <SiteHeader crumbs={[{ label: "Assistant" }]} />
      <Page className="gap-4 md:h-[calc(100svh-52px-2rem)] md:pb-6">
        <PageHeading
          title="Assistant"
          description="Ask the workspace's agent about what's running and why. It works with your permissions."
        />
        <div className="grid min-h-0 flex-1 gap-4 md:grid-cols-[260px_minmax(0,1fr)]">
          <ConversationList
            selected={conversationId}
            onSelect={(id) => open(id)}
          />
          <section
            aria-label="Conversation"
            className="flex h-[70svh] min-h-0 min-w-0 flex-col overflow-hidden rounded-[14px] bg-card ring-1 ring-border md:h-auto"
          >
            <ConversationView
              conversationId={conversationId}
              projectId={conversationId ? undefined : projectId}
              onConversation={(id) => open(id, true)}
              hint={
                !conversationId &&
                (project ? `In ${project.name}` : "About the whole workspace")
              }
            />
          </section>
        </div>
      </Page>
    </>
  )
}
