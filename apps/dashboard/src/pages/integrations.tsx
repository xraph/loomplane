// Integrations: a catalog of everything Loomplane can connect to, and what's
// connected now: the GitHub accounts it reads code through, the repositories
// they let it read, and the cloud accounts it deploys to.
import { BookMarkedIcon, LockIcon, SearchIcon } from "lucide-react"
import { type ReactNode, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionCloudsList,
  useConnectionsList,
  useGithubDisconnect,
  useGithubRepositories,
  useGithubStatus,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { GitHubInstallation } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ConnectDialog, statusLabels } from "@/components/cloud-connections"
import { IntegrationCatalog } from "@/components/integration-catalog"
import { NewProjectDialog } from "@/components/new-project-dialog"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
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
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import { formatDate } from "@/lib/format"
import { cloudHealth, cloudPurposes } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export type IntegrationsTab = "catalog" | "connected"

export function Integrations({
  tab = "catalog",
  onTab,
}: {
  tab?: IntegrationsTab
  onTab?: (tab: IntegrationsTab) => void
} = {}) {
  const [connecting, setConnecting] = useState(false)
  const [connectCloud, setConnectCloud] = useState<string | undefined>()
  const [shown, setShown] = useState<IntegrationsTab>(tab)
  const current = onTab ? tab : shown
  const go = (t: IntegrationsTab) => (onTab ? onTab(t) : setShown(t))
  // The repository a new project starts with, while its dialog is open.
  const [starting, setStarting] = useState<string | null>(null)
  const forms = useQuery(useConnectionCloudsList)
  const github = useQuery(useGithubStatus)
  const clouds = useQuery(useConnectionsList)
  const connectedCount =
    github.data && clouds.data
      ? github.data.installations.length + clouds.data.connections.length
      : undefined

  return (
    <>
      <SiteHeader crumbs={[{ label: "Integrations" }]} />
      <Page className="gap-6">
        <PageHeading
          title="Integrations"
          description="Where Loomplane reads your code from, and the clouds it deploys to."
        />
        <Tabs
          value={current}
          onValueChange={(v) => go(v as IntegrationsTab)}
          className="gap-5"
        >
          <TabsList variant="line">
            <TabsTrigger value="catalog">
              Catalog
              {forms.data && (
                <TabsCount>{forms.data.clouds.length + 1}</TabsCount>
              )}
            </TabsTrigger>
            <TabsTrigger value="connected">
              Connected
              {connectedCount !== undefined && (
                <TabsCount>{connectedCount}</TabsCount>
              )}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="catalog">
            <IntegrationCatalog
              onConnect={(cloud) => {
                setConnectCloud(cloud)
                setConnecting(true)
              }}
              onManage={() => go("connected")}
            />
          </TabsContent>
          <TabsContent value="connected" className="grid gap-4">
            <ConnectedStats />
            <div className="grid items-start gap-3 lg:grid-cols-2">
              <GitHubPanel onStart={setStarting} />
              <CloudsPanel
                onConnect={(cloud) => {
                  setConnectCloud(cloud)
                  setConnecting(true)
                }}
              />
            </div>
          </TabsContent>
        </Tabs>
      </Page>
      <ConnectDialog
        open={connecting}
        onOpenChange={(open) => {
          setConnecting(open)
          if (!open) setConnectCloud(undefined)
        }}
        cloud={connectCloud}
      />
      <NewProjectDialog
        open={starting !== null}
        repository={starting ?? undefined}
        onOpenChange={(open) => {
          if (!open) setStarting(null)
        }}
      />
    </>
  )
}

/** GitHubMark is GitHub's mark, which lucide no longer draws. */
function GitHubMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" className={className}>
      <path
        fill="currentColor"
        d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
      />
    </svg>
  )
}

/* ------------------------------------------------------------------------ */
/* At a glance                                                                */

function Stat({
  label,
  value,
  note,
  tone,
}: {
  label: string
  value: ReactNode
  note: ReactNode
  tone?: "good" | "attention"
}) {
  return (
    <section className="grid gap-1 rounded-[14px] bg-card px-4 py-3.5 ring-1 ring-border">
      <span className="text-[12.5px] text-muted-foreground">{label}</span>
      <span
        className={cn(
          "font-mono text-2xl leading-tight font-medium tabular-nums",
          tone === "good" && "text-overdye-ink",
          tone === "attention" && "text-weld-ink"
        )}
      >
        {value}
      </span>
      <span className="text-[12.5px] text-muted-foreground">{note}</span>
    </section>
  )
}

function ConnectedStats() {
  const github = useQuery(useGithubStatus)
  const clouds = useQuery(useConnectionsList)
  const installs = github.data?.installations.length ?? 0
  const accounts = clouds.data?.connections.length ?? 0
  const health = cloudHealth(clouds.data?.connections ?? [])
  const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {!github.data || !clouds.data ? (
        [0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-[92px] rounded-[14px]" />
        ))
      ) : (
        <>
          <Stat
            label="Connected"
            value={installs + accounts}
            note={
              installs + accounts === 0
                ? "Nothing connected yet"
                : `${plural(installs, "GitHub account")} and ${plural(accounts, "cloud account")}`
            }
          />
          {installs > 0 ? (
            <RepositoryStat />
          ) : (
            <Stat
              label="Repositories"
              value={<span className="text-muted-foreground">Public</span>}
              note={
                github.data.configured
                  ? "Only public ones, until an account is connected"
                  : "Only public ones: no GitHub App is set up"
              }
            />
          )}
          <Stat
            label="Clouds ready"
            value={health.total ? `${health.ready} of ${health.total}` : "None"}
            tone={
              !health.total
                ? undefined
                : health.attention
                  ? "attention"
                  : "good"
            }
            note={
              !health.total
                ? "None connected yet"
                : health.attention
                  ? `${health.attention} ${health.attention === 1 ? "needs" : "need"} attention`
                  : "All checked and ready"
            }
          />
        </>
      )}
    </div>
  )
}

// RepositoryStat asks GitHub, so it's only shown once an account is in.
function RepositoryStat() {
  const repos = useQuery(useGithubRepositories)
  if (repos.status === "error")
    return <Stat label="Repositories" value="?" note="GitHub didn't answer" />
  if (!repos.data) return <Skeleton className="h-[92px] rounded-[14px]" />
  const list = repos.data.repositories
  const hidden = list.filter((r) => r.private).length
  return (
    <Stat
      label="Repositories"
      value={list.length}
      note={
        hidden ? `Loomplane can read, ${hidden} private` : "Loomplane can read"
      }
    />
  )
}

/* ------------------------------------------------------------------------ */
/* GitHub                                                                     */

function Panel({
  title,
  description,
  media,
  action,
  children,
  footer,
}: {
  title: string
  description?: ReactNode
  media?: ReactNode
  action?: ReactNode
  children: ReactNode
  footer?: ReactNode
}) {
  return (
    <section
      aria-label={title}
      className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border"
    >
      <div className="flex items-center gap-2.5 border-b px-4 py-3">
        {media}
        <div className="grid min-w-0 flex-1 gap-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && (
            <p className="text-[12.5px] leading-relaxed text-muted-foreground">
              {description}
            </p>
          )}
        </div>
        {action}
      </div>
      {children}
      {footer && (
        <div className="border-t bg-subtle px-4 py-2.5 text-xs leading-relaxed text-muted-foreground">
          {footer}
        </div>
      )}
    </section>
  )
}

function GitHubPanel({ onStart }: { onStart: (repository: string) => void }) {
  const { can } = useOrganization()
  const status = useQuery(useGithubStatus)
  const installations = status.data?.installations ?? []
  const mayConnect = can("repository:connect")
  return (
    <Panel
      title="GitHub"
      media={
        <span className="flex size-[30px] shrink-0 items-center justify-center rounded-lg bg-foreground text-background">
          <GitHubMark className="size-4" />
        </span>
      }
      action={
        installations.length > 0 &&
        status.data?.installUrl && (
          // A link, not a button: it leaves for GitHub.
          <a
            href={status.data.installUrl}
            className={buttonVariants({ variant: "outline", size: "sm" })}
          >
            Install on another account
          </a>
        )
      }
      footer="Public repositories need nothing here. For a private one, Loomplane gets a read-only token for that one repository, for as long as an analysis takes, and keeps none."
    >
      {status.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={status.error}
            fallback="The GitHub connection couldn't be loaded."
            retry={status.refetch}
          />
        </div>
      ) : !status.data ? (
        <div className="p-4">
          <Skeleton className="h-32 w-full" />
        </div>
      ) : !status.data.configured ? (
        <div className="p-4">
          <SetUpGitHub />
        </div>
      ) : installations.length === 0 ? (
        <Empty variant="inline">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <GitHubMark className="size-4" />
            </EmptyMedia>
            <EmptyTitle>No GitHub accounts yet</EmptyTitle>
            <EmptyDescription>
              {mayConnect
                ? "Install the App on an organization or your own account, and choose which repositories Loomplane may read. GitHub sends you back here."
                : "An admin can install the App on an organization."}
            </EmptyDescription>
          </EmptyHeader>
          {status.data.installUrl && (
            <EmptyContent>
              <a href={status.data.installUrl} className={buttonVariants()}>
                <GitHubMark className="size-4" />
                Install on GitHub
              </a>
            </EmptyContent>
          )}
        </Empty>
      ) : (
        <>
          <ul className="divide-y border-b">
            {installations.map((inst) => (
              <InstallationRow
                key={inst.id}
                installation={inst}
                mayConnect={mayConnect}
              />
            ))}
          </ul>
          <Repositories onStart={onStart} />
        </>
      )}
    </Panel>
  )
}

function InstallationRow({
  installation: inst,
  mayConnect,
}: {
  installation: GitHubInstallation
  mayConnect: boolean
}) {
  const [disconnecting, setDisconnecting] = useState(false)
  return (
    <li className="flex flex-wrap items-center gap-x-3.5 gap-y-2 px-4 py-2.5">
      <AccountAvatar account={inst.account} src={inst.avatarUrl} />
      <span className="grid min-w-0 flex-[1_1_160px] gap-0.5">
        <span className="truncate text-[13.5px] font-medium">
          {inst.account}
        </span>
        <span className="text-xs text-muted-foreground">
          {inst.accountType === "User" ? "Personal" : "Organization"} ·
          connected{" "}
          <time dateTime={inst.createdAt}>{formatDate(inst.createdAt)}</time>
        </span>
      </span>
      <StatusPill tone="live">Connected</StatusPill>
      {inst.manageUrl && (
        <a
          href={inst.manageUrl}
          target="_blank"
          rel="noreferrer"
          className={buttonVariants({ variant: "outline", size: "sm" })}
        >
          Choose repositories
        </a>
      )}
      {mayConnect && (
        <Button
          variant="outline"
          size="sm"
          className="text-destructive"
          onClick={() => setDisconnecting(true)}
        >
          Disconnect
        </Button>
      )}
      <Disconnect
        installation={inst}
        open={disconnecting}
        onOpenChange={setDisconnecting}
      />
    </li>
  )
}

function Repositories({ onStart }: { onStart: (repository: string) => void }) {
  const { can } = useOrganization()
  const repos = useQuery(useGithubRepositories)
  const projects = useQuery(useProjectsList)
  const [query, setQuery] = useState("")
  const all = repos.data?.repositories ?? []
  const q = query.trim().toLowerCase()
  const shown = all.filter((r) => !q || r.fullName.toLowerCase().includes(q))
  const projectFor = (fullName: string) =>
    projects.data?.projects.find((p) => p.repositories.includes(fullName))
  const mayCreate = can("project:create")

  if (repos.status === "error")
    return (
      <div className="p-4">
        <QueryError
          error={repos.error}
          fallback="The repositories couldn't be listed."
          retry={repos.refetch}
        />
      </div>
    )
  if (!repos.data)
    return (
      <div className="grid gap-2 p-4">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    )
  if (all.length === 0)
    return (
      <Empty variant="inline">
        <EmptyHeader>
          <EmptyTitle>No repositories available</EmptyTitle>
          <EmptyDescription>
            Choose repositories in the App&apos;s settings on GitHub.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  return (
    <>
      <div className="border-b px-3 py-2.5">
        <label className="relative flex items-center">
          <SearchIcon className="pointer-events-none absolute left-2.5 size-3.5 text-muted-foreground" />
          <input
            type="search"
            aria-label="Search repositories"
            placeholder={`Search ${all.length} ${all.length === 1 ? "repository" : "repositories"}`}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 w-full rounded-[9px] border border-transparent bg-subtle pr-2.5 pl-8 text-[12.5px] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
          />
        </label>
      </div>
      {shown.length === 0 ? (
        <p className="px-4 py-4 text-[13px] text-muted-foreground">
          No repository matches &ldquo;{query.trim()}&rdquo;.{" "}
          <button
            type="button"
            className="font-medium text-brand-ink hover:underline"
            onClick={() => setQuery("")}
          >
            Clear search
          </button>
        </p>
      ) : (
        <ul className="max-h-[26rem] divide-y overflow-y-auto">
          {shown.map((r) => {
            const project = projectFor(r.fullName)
            return (
              <li
                key={r.fullName}
                className="flex items-center gap-2.5 px-4 py-2"
              >
                {r.private ? (
                  <LockIcon
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-label="Private"
                  />
                ) : (
                  <BookMarkedIcon
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-label="Public"
                  />
                )}
                <a
                  href={r.url}
                  target="_blank"
                  rel="noreferrer"
                  className="min-w-0 flex-1 truncate font-mono text-[12.5px] hover:underline"
                >
                  {r.fullName}
                </a>
                <span className="hidden truncate text-xs text-muted-foreground sm:inline">
                  {project ? `In ${project.name}` : "Not in a project"}
                </span>
                {project ? (
                  <Link
                    to="/projects/$id"
                    params={{ id: project.id }}
                    className="shrink-0 text-[12.5px] font-medium text-brand-ink hover:underline"
                  >
                    Open project
                  </Link>
                ) : (
                  mayCreate && (
                    <button
                      type="button"
                      onClick={() => onStart(r.fullName)}
                      className="shrink-0 text-[12.5px] font-medium text-brand-ink hover:underline"
                    >
                      Project
                    </button>
                  )
                )}
              </li>
            )
          })}
        </ul>
      )}
    </>
  )
}

/* ------------------------------------------------------------------------ */
/* Clouds                                                                     */

const accountDot: Record<string, string> = {
  ready: "bg-overdye",
  incomplete: "bg-weld",
  failed: "bg-failed",
}

function CloudsPanel({ onConnect }: { onConnect: (cloud: string) => void }) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const forms = useQuery(useConnectionCloudsList)
  const connections = useQuery(useConnectionsList)
  const list = connections.data?.connections ?? []
  // Providers with accounts first, then the rest, each in the API's order.
  const providers = [...(forms.data?.clouds ?? [])].sort(
    (a, b) =>
      Number(list.some((c) => c.cloud === b.cloud)) -
      Number(list.some((c) => c.cloud === a.cloud))
  )
  return (
    <Panel
      title="Clouds"
      description="Where Loomplane deploys. Each credential is checked with its cloud before it's kept, stored encrypted, and never shown again."
    >
      {forms.status === "error" || connections.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={forms.error ?? connections.error}
            fallback="Cloud accounts couldn't be loaded."
            retry={() =>
              Promise.allSettled([forms.refetch(), connections.refetch()])
            }
          />
        </div>
      ) : !forms.data || !connections.data ? (
        <div className="p-4">
          <Skeleton className="h-48 w-full" />
        </div>
      ) : (
        <ul className="divide-y">
          {providers.map((f) => {
            const mine = list.filter((c) => c.cloud === f.cloud)
            return (
              <li key={f.cloud} className="grid gap-1.5 px-4 py-2.5">
                <div className="flex items-center gap-2.5">
                  <CloudGlyph cloud={f.cloud} size="sm" />
                  <span className="grid min-w-0 flex-1 gap-px">
                    <span className="truncate text-[13.5px] font-semibold">
                      {f.title}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {cloudPurposes[f.cloud] ?? "Deploy services to it"}
                    </span>
                  </span>
                  {manage && (
                    <Button
                      size="sm"
                      variant={mine.length ? "outline" : "default"}
                      onClick={() => onConnect(f.cloud)}
                    >
                      {mine.length ? "Add" : "Connect"}
                    </Button>
                  )}
                </div>
                {mine.length === 0 ? (
                  <span className="ml-[38px] text-[12.5px] text-muted-foreground">
                    Not connected.
                  </span>
                ) : (
                  mine.map((c) => (
                    <Link
                      key={c.id}
                      to="/clouds/accounts/$id"
                      params={{ id: c.id }}
                      className="ml-[38px] flex items-center gap-2 rounded-md text-[12.5px] hover:underline"
                    >
                      <span
                        aria-hidden="true"
                        className={cn(
                          "size-1.5 shrink-0 rounded-full",
                          accountDot[c.status] ?? "bg-muted-foreground/50"
                        )}
                      />
                      <span className="truncate font-medium">{c.name}</span>
                      <span className="truncate text-muted-foreground">
                        {c.account}
                      </span>
                      <span className="flex-1" />
                      <span
                        className={cn(
                          "shrink-0",
                          c.status === "ready"
                            ? "text-overdye-ink"
                            : c.status === "failed"
                              ? "text-destructive"
                              : "text-weld-ink"
                        )}
                      >
                        {statusLabels[c.status] ?? c.status}
                      </span>
                    </Link>
                  ))
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

function AccountAvatar({
  account,
  src,
  className,
}: {
  account: string
  src?: string
  className?: string
}) {
  return (
    <Avatar className={cn("size-9 rounded-lg after:rounded-lg", className)}>
      {src && <AvatarImage src={src} alt="" className="rounded-lg" />}
      <AvatarFallback className="rounded-lg text-xs">
        {account.slice(0, 2).toUpperCase()}
      </AvatarFallback>
    </Avatar>
  )
}

/**
 * SetUpGitHub is for a Loomplane with no GitHub App: whoever runs it
 * creates one, once, for the whole instance.
 */

/**
 * SetUpGitHub is for a Loomplane with no GitHub App: whoever runs it
 * creates one, once, for the whole instance.
 */
function SetUpGitHub() {
  return (
    <div className="grid flex-1 content-start gap-4 rounded-xl border border-dashed p-5">
      <div className="grid gap-1">
        <h3 className="font-medium">Set up the GitHub App</h3>
        <p className="text-sm text-muted-foreground">
          Until it&apos;s set up, Loomplane reads public repositories only.
          Whoever runs this Loomplane creates the App once, for every
          organization on it.
        </p>
      </div>
      <ol className="grid gap-3 text-sm">
        <Step n={1}>
          Create it from the Loomplane checkout, for the GitHub organization
          that should own it:
          <code className="mt-1.5 block overflow-x-auto rounded-lg border bg-muted/50 px-3 py-2 font-mono text-xs whitespace-nowrap">
            go run ./scripts/github-app -org your-org
          </code>
        </Step>
        <Step n={2}>
          Choose Create GitHub App on GitHub. Its settings land in
          config.local.yaml, with the key beside it.
        </Step>
        <Step n={3}>Restart Loomplane, and install the App from here.</Step>
      </ol>
      <p className="text-xs text-muted-foreground">
        docs/github-app.md has the same settings, for creating the App by hand.
      </p>
    </div>
  )
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[1.5rem_minmax(0,1fr)] gap-2">
      <span className="flex size-6 items-center justify-center rounded-full border text-xs text-muted-foreground tabular-nums">
        {n}
      </span>
      <span className="pt-0.5">{children}</span>
    </li>
  )
}

function Disconnect({
  installation,
  open,
  onOpenChange,
}: {
  installation: GitHubInstallation
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const remove = useMutation(useGithubDisconnect)
  const invalidate = useInvalidate()

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: installation.id } })
      toast.success(`Disconnected ${installation.account}`)
    } catch (err) {
      toast.error(messageOf(err, "It wasn't disconnected."))
    } finally {
      onOpenChange(false)
      invalidate(useGithubStatus)
      invalidate(useGithubRepositories)
    }
  }

  // Controlled: Base UI's AlertDialogAction doesn't close the dialog.
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Disconnect {installation.account}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane stops reading its private repositories. Analyses already
            done stay. The App stays installed on GitHub; uninstall it there if
            you want it gone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Disconnect
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
