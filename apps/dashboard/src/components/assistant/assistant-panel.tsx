// The agent in a slide-over, from any page: ⌘J, the command menu, or a
// page's own button. It holds one conversation, scoped to the project page
// it was opened from unless you ask about the whole workspace.
import { ArrowUpRightIcon, PlusIcon, XIcon } from "lucide-react"

import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { ConversationView } from "@/components/assistant/conversation-view"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export type AssistantPanelProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The project a new conversation is about; none is the whole workspace. */
  projectId?: string
  /** The conversation the panel shows; none starts a new one. */
  conversationId?: string
  /** onConversation hears the ID a new conversation got. */
  onConversation?: (id: string) => void
  /** onScope changes what the next new conversation is about. */
  onScope?: (projectId: string | undefined) => void
  /** onNew leaves the conversation for a new one. */
  onNew?: () => void
  /** Whether projectId is what the open conversation is about: it is for
   * one the panel started, and unknown for one it was handed. */
  scoped?: boolean
}

export function AssistantPanel({
  open,
  onOpenChange,
  projectId,
  conversationId,
  onConversation,
  onScope,
  onNew,
  scoped = true,
}: AssistantPanelProps) {
  const projects = useQuery(useProjectsList)
  const project = projectId
    ? projects.data?.projects.find((p) => p?.id === projectId)
    : undefined
  const scope = projectId
    ? `In ${project?.name ?? "this project"}`
    : "The whole workspace"

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        showCloseButton={false}
        className="gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[440px]"
      >
        <SheetHeader className="gap-2 border-b px-3 py-3">
          <div className="flex items-center gap-2">
            <SheetTitle className="flex-1 text-[15px] font-semibold">
              Assistant
            </SheetTitle>
            <Button
              variant="ghost"
              size="sm"
              onClick={onNew}
              disabled={!conversationId}
            >
              <PlusIcon />
              New conversation
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Close"
              onClick={() => onOpenChange(false)}
            >
              <XIcon />
            </Button>
          </div>
          <SheetDescription className="sr-only">
            Ask the workspace&apos;s agent. It works with your permissions.
          </SheetDescription>
          <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
            {(scoped || !conversationId) && (
              <Badge variant="outline" className="rounded-full">
                {scope}
              </Badge>
            )}
            {projectId && !conversationId && (
              <button
                type="button"
                className="text-primary underline-offset-2 hover:underline"
                onClick={() => onScope?.(undefined)}
              >
                Ask about the whole workspace
              </button>
            )}
            {conversationId && (
              <Link
                to="/assistant"
                search={{ c: conversationId }}
                onClick={() => onOpenChange(false)}
                className={cn(
                  buttonVariants({ variant: "link", size: "sm" }),
                  "ml-auto h-auto px-0"
                )}
              >
                Open in Assistant
                <ArrowUpRightIcon />
              </Link>
            )}
          </div>
        </SheetHeader>
        <ConversationView
          compact
          className="min-h-0"
          conversationId={conversationId}
          projectId={projectId}
          onConversation={onConversation}
        />
      </SheetContent>
    </Sheet>
  )
}
