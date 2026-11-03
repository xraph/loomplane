// Connecting an integration on a page of its own, with a switcher to move
// to any other. What you run yourself (an SSH server with Docker, an image
// registry, a Kubernetes cluster) gets presets and a guide to the commands
// that fetch the rest; every other cloud gets its connect form, with its
// help beside it. Leaving a form half filled asks first.
import { CheckIcon, ChevronsUpDownIcon } from "lucide-react"
import { useRef } from "react"

import {
  useConnectionCloudsList,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import type { CloudForm, Connection } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ConnectPanel } from "@/components/cloud-connections"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import {
  SelfHostedConnect,
  selfHostedTitles,
} from "@/components/self-hosted-connect"
import { SiteHeader } from "@/components/site-header"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { isSelfHosted } from "@/lib/connect-presets"
import { cloudPurposes, serverKinds } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { useBlocker, useNavigate } from "@/lib/router"

const intros: Record<string, string> = {
  server:
    "Loomplane runs your services on a machine's Docker, over SSH. Pick where it is, and the form fills what it can.",
  registry:
    "Loomplane pushes the images it builds here, and your servers pull them from it.",
  kubernetes:
    "Loomplane deploys into one namespace of your cluster. Start from where the cluster runs, then adjust.",
}

function titleOf(cloud: string, entry?: CloudForm): string {
  return (
    entry?.title ||
    (isSelfHosted(cloud) ? selfHostedTitles[cloud] : cloudLabel(cloud))
  )
}

export function ConnectCloudPage({ cloud }: { cloud: string }) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const forms = useQuery(useConnectionCloudsList)
  const entry = forms.data?.clouds.find((c) => c.cloud === cloud)
  const title = titleOf(cloud, entry)
  const unknown = !!forms.data && !entry && !isSelfHosted(cloud)

  // Whether the form has anything typed in it. A ref, not state: the
  // blocker reads it as it decides, and a connection that went through
  // clears it before moving on.
  const dirty = useRef(false)
  const onDirtyChange = (d: boolean) => {
    dirty.current = d
  }
  const leave = useBlocker({
    shouldBlockFn: () => dirty.current,
    enableBeforeUnload: () => dirty.current,
    withResolver: true,
  })
  const connected = (c: Connection) => {
    dirty.current = false
    void navigate({ to: "/clouds/accounts/$id", params: { id: c.id } })
  }
  const cancel = () => void navigate({ to: "/integrations" })

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: `Connect ${title}` },
        ]}
      />
      <Page className="gap-6">
        <PageHeading
          eyebrow={<CloudGlyph cloud={cloud} size="md" className="mb-2" />}
          title={`Connect ${title}`}
          description={
            intros[cloud] ??
            `Loomplane checks the credential with ${title} before keeping it, and stores it encrypted.`
          }
          actions={<Switcher cloud={cloud} />}
        />
        {!can("cloud_account:manage") ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>Only admins connect infrastructure</EmptyTitle>
              <EmptyDescription>
                Ask an admin of this workspace to connect it, or to give you the
                permission.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : forms.status === "error" ? (
          <QueryError
            error={forms.error}
            fallback="The list of integrations couldn't be loaded."
            retry={forms.refetch}
          />
        ) : unknown ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>Loomplane can't connect to {cloud}</EmptyTitle>
              <EmptyDescription>
                It isn't one of the integrations this Loomplane offers. Its
                plugin may have been removed.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Switcher cloud={cloud} />
            </EmptyContent>
          </Empty>
        ) : isSelfHosted(cloud) ? (
          <SelfHostedConnect
            cloud={cloud}
            layout="page"
            onConnected={connected}
            onCancel={cancel}
            onDirtyChange={onDirtyChange}
          />
        ) : !entry ? (
          <Skeleton className="h-80 w-full rounded-[14px]" />
        ) : (
          <ManagedCloudForm
            entry={entry}
            onConnected={connected}
            onCancel={cancel}
            onDirtyChange={onDirtyChange}
          />
        )}
      </Page>
      {/* Controlled: Base UI's AlertDialogAction doesn't close the dialog. */}
      <AlertDialog
        open={leave.status === "blocked"}
        onOpenChange={(open) => {
          if (!open && leave.status === "blocked") leave.reset()
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Leave without connecting?</AlertDialogTitle>
            <AlertDialogDescription>
              Nothing is saved until Loomplane checks and connects it, so what
              you&apos;ve typed for {title} is lost.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (leave.status === "blocked") leave.proceed()
              }}
            >
              Leave
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

/**
 * Switcher moves to any other integration without going back to the
 * catalog, grouped the way the catalog groups them.
 */
function Switcher({ cloud }: { cloud: string }) {
  const navigate = useNavigate()
  const forms = useQuery(useConnectionCloudsList)
  const connections = useQuery(useConnectionsList)
  const list = forms.data?.clouds ?? []
  const count = (c: string) =>
    (connections.data?.connections ?? []).filter((x) => x.cloud === c).length
  const groups = [
    {
      label: "Clouds",
      items: list.filter((f) => !serverKinds.has(f.cloud)),
    },
    {
      label: "Servers",
      items: list.filter((f) => serverKinds.has(f.cloud)),
    },
  ].filter((g) => g.items.length > 0)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button variant="outline" aria-label="Connect something else" />
        }
      >
        <CloudGlyph cloud={cloud} size="xs" />
        {titleOf(
          cloud,
          list.find((c) => c.cloud === cloud)
        )}
        <ChevronsUpDownIcon className="text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        {groups.map((g, i) => (
          <DropdownMenuGroup key={g.label}>
            {i > 0 && <DropdownMenuSeparator />}
            <DropdownMenuLabel>{g.label}</DropdownMenuLabel>
            {g.items.map((f) => {
              const n = count(f.cloud)
              return (
                <DropdownMenuItem
                  key={f.cloud}
                  onClick={() =>
                    void navigate({
                      to: "/integrations/connect/$cloud",
                      params: { cloud: f.cloud },
                    })
                  }
                >
                  <CloudGlyph cloud={f.cloud} size="xs" />
                  <span className="min-w-0 flex-1 truncate">{f.title}</span>
                  {n > 0 && (
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {n} connected
                    </span>
                  )}
                  {f.cloud === cloud && <CheckIcon />}
                </DropdownMenuItem>
              )
            })}
          </DropdownMenuGroup>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * ManagedCloudForm is a managed cloud's connect form, the same one the
 * dialog shows, with what the provider says about its credential beside it.
 */
function ManagedCloudForm({
  entry,
  onConnected,
  onCancel,
  onDirtyChange,
}: {
  entry: CloudForm
  onConnected: (c: Connection) => void
  onCancel: () => void
  onDirtyChange: (dirty: boolean) => void
}) {
  return (
    <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
      <div className="min-w-0">
        <ConnectPanel
          cloud={entry.cloud}
          onCancel={onCancel}
          onConnected={onConnected}
          onDirtyChange={onDirtyChange}
        />
      </div>
      <aside className="grid gap-3 lg:sticky lg:top-16">
        <section className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border">
          <div className="border-b px-4 py-3">
            <h2 className="text-sm font-semibold">About {entry.title}</h2>
            {cloudPurposes[entry.cloud] && (
              <p className="text-[12.5px] text-muted-foreground">
                {cloudPurposes[entry.cloud]}
              </p>
            )}
          </div>
          <div className="grid gap-2.5 px-4 py-3.5 text-[12.5px] leading-relaxed text-muted-foreground">
            {entry.help && <p>{entry.help}</p>}
            <p>
              {entry.methods.includes("oauth")
                ? `Signing in is quickest: you approve Loomplane on ${entry.title} and come back here.`
                : entry.methods.includes("keyless")
                  ? `Keyless keeps no secret: ${entry.title} trusts Loomplane's identity instead.`
                  : "Loomplane checks each permission without changing anything, and says which are missing."}
            </p>
          </div>
        </section>
      </aside>
    </div>
  )
}
