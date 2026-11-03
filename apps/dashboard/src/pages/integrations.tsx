// Integrations: a catalog of everything Loomplane can connect to, and what's
// connected now: the GitHub accounts it reads code through, the repositories
// they let it read, and the cloud accounts it deploys to.
import { BookMarkedIcon, LockIcon, SearchIcon } from "lucide-react"
import { type ReactNode, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useBitbucketDisconnect,
  useBitbucketRepositories,
  useBitbucketStatus,
  useConnectionCloudsList,
  useConnectionsList,
  useGithubDisconnect,
  useGithubRepositories,
  useGithubStatus,
  useGitlabDisconnect,
  useGitlabRemoveHost,
  useGitlabRepositories,
  useGitlabStartSignIn,
  useGitlabStatus,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  BitbucketWorkspaceView,
  GitHubInstallation,
  GitLabConnectionView,
  GitLabHostView,
  GitLabSignInStart,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { BitbucketMark } from "@/components/bitbucket-mark"
import {
  BitbucketWorkspaceDialog,
  useBitbucketSignIn,
} from "@/components/bitbucket-workspace-dialog"
import { CloudGlyph } from "@/components/cloud-glyph"
import { statusLabels } from "@/components/cloud-connections"
import { GitHubMark } from "@/components/github-mark"
import { GitLabHostDialog } from "@/components/gitlab-host-dialog"
import { GitLabMark } from "@/components/gitlab-mark"
import { GitLabWebhook } from "@/components/gitlab-webhook"
import { IntegrationCatalog } from "@/components/integration-catalog"
import { NewProjectDialog } from "@/components/new-project-dialog"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { RepositoryListings } from "@/components/repository-listings"
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
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import {
  canReadBitbucket,
  cloudHealth,
  cloudPurposes,
} from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"
import { ProviderResources } from "@/pages/provider-resources"

export type IntegrationsTab = "catalog" | "connected" | "resources"

export function Integrations({
  tab = "catalog",
  onTab,
  connection,
}: {
  tab?: IntegrationsTab
  onTab?: (tab: IntegrationsTab) => void
  /** The account the Resources tab opens filtered to. */
  connection?: string
} = {}) {
  const navigate = useNavigate()
  // Each integration connects on a page of its own.
  const connect = (cloud: string) =>
    void navigate({ to: "/integrations/connect/$cloud", params: { cloud } })
  const [shown, setShown] = useState<IntegrationsTab>(tab)
  const current = onTab ? tab : shown
  const go = (t: IntegrationsTab) => (onTab ? onTab(t) : setShown(t))
  // The repository a new project starts with, while its dialog is open.
  const [starting, setStarting] = useState<string | null>(null)
  const forms = useQuery(useConnectionCloudsList)
  const github = useQuery(useGithubStatus)
  const gitlab = useQuery(useGitlabStatus)
  const bitbucket = useQuery(useBitbucketStatus)
  const clouds = useQuery(useConnectionsList)
  // GitLab and Bitbucket are counted once they have answered, or failed to.
  const connectedCount =
    github.data &&
    clouds.data &&
    (gitlab.data || gitlab.status === "error") &&
    (bitbucket.data || bitbucket.status === "error")
      ? github.data.installations.length +
        (gitlab.data?.connections.length ?? 0) +
        (bitbucket.data?.workspaces?.length ?? 0) +
        clouds.data.connections.length
      : undefined

  return (
    <>
      <SiteHeader
        crumbs={
          current === "resources"
            ? [
                { label: "Integrations", to: "/integrations" },
                { label: "Resources" },
              ]
            : [{ label: "Integrations" }]
        }
      />
      <Page className="gap-6">
        <PageHeading
          title="Integrations"
          description="Where Loomplane reads your code from, the clouds it deploys to, and the resources those clouds run for you."
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
            <TabsTrigger value="resources">Resources</TabsTrigger>
          </TabsList>
          <TabsContent value="catalog">
            <IntegrationCatalog
              onConnect={connect}
              onManage={() => go("connected")}
            />
          </TabsContent>
          <TabsContent value="connected" className="grid gap-4">
            <ConnectedStats />
            <div className="grid items-start gap-3 lg:grid-cols-2">
              <div className="grid gap-3">
                <GitHubPanel onStart={setStarting} />
                <GitLabPanel />
                <BitbucketPanel />
              </div>
              <CloudsPanel onConnect={connect} />
            </div>
          </TabsContent>
          <TabsContent value="resources">
            {current === "resources" && (
              <ProviderResources
                key={connection ?? "all"}
                connection={connection}
              />
            )}
          </TabsContent>
        </Tabs>
      </Page>
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
  const gitlab = useQuery(useGitlabStatus)
  const bitbucket = useQuery(useBitbucketStatus)
  const clouds = useQuery(useConnectionsList)
  const installs = github.data?.installations.length ?? 0
  const gitlabs = gitlab.data?.connections.length ?? 0
  const workspaces = bitbucket.data?.workspaces?.length ?? 0
  const accounts = clouds.data?.connections.length ?? 0
  const health = cloudHealth(clouds.data?.connections ?? [])
  const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`
  // A GitLab or Bitbucket that can't be reached counts as none, as the
  // tab's count does.
  const loaded =
    !!github.data &&
    !!clouds.data &&
    (!!gitlab.data || gitlab.status === "error") &&
    (!!bitbucket.data || bitbucket.status === "error")
  const total = installs + gitlabs + workspaces + accounts
  // GitHub and clouds are always named; the others only once they have one.
  const parts = [
    plural(installs, "GitHub account"),
    ...(gitlabs > 0 ? [plural(gitlabs, "GitLab account")] : []),
    ...(workspaces > 0 ? [plural(workspaces, "Bitbucket workspace")] : []),
    plural(accounts, "cloud account"),
  ]
  const summary = `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {!loaded ? (
        [0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-[92px] rounded-[14px]" />
        ))
      ) : (
        <>
          <Stat
            label="Connected"
            value={total}
            note={total === 0 ? "Nothing connected yet" : summary}
          />
          {installs > 0 || gitlabs > 0 || canReadBitbucket(bitbucket.data) ? (
            <RepositoryStat
              github={installs > 0}
              gitlab={gitlabs > 0}
              bitbucket={canReadBitbucket(bitbucket.data)}
            />
          ) : (
            <Stat
              label="Repositories"
              value={<span className="text-muted-foreground">Public</span>}
              note={
                github.data?.configured
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

// RepositoryStat asks the hosts, so it's only shown once an account is in.
function RepositoryStat({
  github,
  gitlab,
  bitbucket,
}: {
  github: boolean
  gitlab: boolean
  bitbucket: boolean
}) {
  return (
    <RepositoryListings github={github} gitlab={gitlab} bitbucket={bitbucket}>
      {(listings) => {
        const failed = listings.find((l) => l.error)
        if (failed)
          return (
            <Stat
              label="Repositories"
              value="?"
              note={
                listings.length === 1
                  ? `${failed.name} didn't answer`
                  : "A host didn't answer"
              }
            />
          )
        if (listings.some((l) => l.loading))
          return <Skeleton className="h-[92px] rounded-[14px]" />
        const all = listings.flatMap((l) => l.repositories)
        const hidden = all.filter((r) => r.private).length
        return (
          <Stat
            label="Repositories"
            value={all.length}
            note={
              hidden
                ? `Loomplane can read, ${hidden} private`
                : "Loomplane can read"
            }
          />
        )
      }}
    </RepositoryListings>
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
/* GitLab                                                                     */

/**
 * useGitLabSignIn starts a GitLab sign-in on a host and leaves for GitLab,
 * which sends the person back to the callback page.
 */
function useGitLabSignIn() {
  const start = useMutation(useGitlabStartSignIn)
  const [host, setHost] = useState<string | null>(null)
  const signIn = async (hostId: string) => {
    setHost(hostId)
    try {
      const started = (await start.mutateAsync({
        body: { hostId },
      })) as GitLabSignInStart
      window.location.assign(started.authorizeUrl)
    } catch (err) {
      setHost(null)
      toast.error(messageOf(err, "Sign-in couldn't be started."))
    }
  }
  return { signIn, pendingHost: start.isPending ? host : null }
}

function GitLabPanel() {
  const { can } = useOrganization()
  const status = useQuery(useGitlabStatus)
  const { signIn, pendingHost } = useGitLabSignIn()
  const [adding, setAdding] = useState(false)
  const data = status.data
  const hosts = data?.hosts ?? []
  const connections = data?.connections ?? []
  // The server says whether this person may connect, and their role must too.
  const mayConnect = !!data?.canConnect && can("repository:connect")
  const mayAddHost = !!data?.organizationHosts && mayConnect
  // gitlab.com, until someone has signed in to it, has a button of its own.
  const home =
    connections.length === 0
      ? hosts.find((h) => h.instance && h.host === "gitlab.com")
      : undefined
  const listed = hosts.filter((h) => h !== home)
  const addHost = mayAddHost && (
    <Button
      variant="outline"
      size={connections.length ? "sm" : "default"}
      onClick={() => setAdding(true)}
    >
      Add a self-managed GitLab
    </Button>
  )

  return (
    <Panel
      title="GitLab"
      media={
        <span className="flex size-[30px] shrink-0 items-center justify-center rounded-lg bg-foreground text-background">
          <GitLabMark className="size-4" />
        </span>
      }
      action={connections.length > 0 && addHost}
    >
      {status.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={status.error}
            fallback="The GitLab connection couldn't be loaded."
            retry={status.refetch}
          />
        </div>
      ) : !data ? (
        <div className="p-4">
          <Skeleton className="h-32 w-full" />
        </div>
      ) : hosts.length === 0 &&
        !data.organizationHosts &&
        !connections.length ? (
        <p className="px-4 py-4 text-[13px] leading-relaxed text-muted-foreground">
          Public gitlab.com projects can still be read. Ask your operator to set
          up GitLab to read private ones.
        </p>
      ) : (
        <>
          {connections.length === 0 ? (
            <Empty variant="inline">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <GitLabMark className="size-4" />
                </EmptyMedia>
                <EmptyTitle>No GitLab accounts yet</EmptyTitle>
                <EmptyDescription>
                  {mayConnect
                    ? "Sign in with GitLab to let Loomplane read the projects you can see there."
                    : "An admin can connect a GitLab."}
                </EmptyDescription>
              </EmptyHeader>
              {mayConnect && (home || addHost) && (
                <EmptyContent>
                  {home && (
                    <Button
                      disabled={pendingHost !== null}
                      onClick={() => void signIn(home.id)}
                    >
                      {pendingHost === home.id ? (
                        <Spinner />
                      ) : (
                        <GitLabMark className="size-4" />
                      )}
                      Connect GitLab.com
                    </Button>
                  )}
                  {addHost}
                </EmptyContent>
              )}
            </Empty>
          ) : (
            <ul className="divide-y">
              {connections.map((c) => (
                <GitLabConnectionRow
                  key={c.id}
                  connection={c}
                  host={hosts.find((h) => h.host === c.host)}
                  mayConnect={mayConnect}
                  onSignIn={signIn}
                  signingIn={pendingHost !== null}
                />
              ))}
            </ul>
          )}
          {listed.length > 0 && (
            <ul
              className={cn("divide-y", connections.length > 0 && "border-t")}
            >
              {listed.map((h) => (
                <GitLabHostRow
                  key={h.id}
                  host={h}
                  mayConnect={mayConnect}
                  mayRemove={mayAddHost && !h.instance}
                  disabled={pendingHost !== null}
                  pending={pendingHost === h.id}
                  onConnect={() => void signIn(h.id)}
                />
              ))}
            </ul>
          )}
        </>
      )}
      <GitLabHostDialog
        open={adding}
        onOpenChange={setAdding}
        callbackUrl={data?.callbackUrl ?? ""}
      />
    </Panel>
  )
}

function GitLabHostRow({
  host,
  mayConnect,
  mayRemove,
  disabled,
  pending,
  onConnect,
}: {
  host: GitLabHostView
  mayConnect: boolean
  mayRemove: boolean
  disabled: boolean
  pending: boolean
  onConnect: () => void
}) {
  const [removing, setRemoving] = useState(false)
  return (
    <li className="flex items-center gap-3 px-4 py-2.5">
      <span className="min-w-0 flex-1 truncate text-[13.5px] font-medium">
        {host.host}
      </span>
      {mayConnect && (
        <Button
          variant="outline"
          size="sm"
          aria-label={`Connect ${host.host}`}
          disabled={disabled}
          onClick={onConnect}
        >
          {pending && <Spinner />}
          Connect
        </Button>
      )}
      {mayRemove && (
        <>
          <Button
            variant="outline"
            size="sm"
            className="text-destructive"
            aria-label={`Remove ${host.host}`}
            onClick={() => setRemoving(true)}
          >
            Remove
          </Button>
          <RemoveGitLabHost
            host={host}
            open={removing}
            onOpenChange={setRemoving}
          />
        </>
      )}
    </li>
  )
}

function RemoveGitLabHost({
  host,
  open,
  onOpenChange,
}: {
  host: GitLabHostView
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const remove = useMutation(useGitlabRemoveHost)
  const invalidate = useInvalidate()
  const [error, setError] = useState<string | null>(null)

  const confirm = async () => {
    setError(null)
    try {
      await remove.mutateAsync({ path: { id: host.id } })
      invalidate(useGitlabStatus)
      toast.success(`Removed ${host.host}`)
      onOpenChange(false)
    } catch (err) {
      // The server refuses while someone is still connected through it.
      setError(messageOf(err, "It wasn't removed."))
    }
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setError(null)
        onOpenChange(next)
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {host.host}?</AlertDialogTitle>
          <AlertDialogDescription>
            People can no longer sign in to it from Loomplane. You can add it
            again later.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function GitLabConnectionRow({
  connection: c,
  host,
  mayConnect,
  onSignIn,
  signingIn,
}: {
  connection: GitLabConnectionView
  host?: GitLabHostView
  mayConnect: boolean
  onSignIn: (hostId: string) => Promise<void>
  signingIn: boolean
}) {
  const [disconnecting, setDisconnecting] = useState(false)
  const [webhook, setWebhook] = useState(false)
  const who = `${c.username} on ${c.host}`
  return (
    <li className="grid gap-2.5 px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
        <AccountAvatar account={c.username} src={c.avatarUrl} />
        <span className="grid min-w-0 flex-[1_1_160px] gap-0.5">
          <span className="truncate text-[13.5px] font-medium">
            {c.username}
          </span>
          <span className="text-xs text-muted-foreground">
            <span>{c.host}</span> · connected{" "}
            <time dateTime={c.createdAt}>{formatDate(c.createdAt)}</time>
          </span>
        </span>
        {c.lapsed ? (
          <StatusPill tone="waiting">Sign-in needed</StatusPill>
        ) : (
          <StatusPill tone="live">Connected</StatusPill>
        )}
        {c.lapsed && mayConnect && host && (
          <Button
            size="sm"
            aria-label={`Sign in again as ${who}`}
            disabled={signingIn}
            onClick={() => void onSignIn(host.id)}
          >
            Sign in again
          </Button>
        )}
        {mayConnect && (
          <Button
            variant="outline"
            size="sm"
            aria-label={`Webhook for ${who}`}
            aria-expanded={webhook}
            onClick={() => setWebhook((v) => !v)}
          >
            Webhook
          </Button>
        )}
        {mayConnect && (
          <Button
            variant="outline"
            size="sm"
            className="text-destructive"
            aria-label={`Disconnect ${who}`}
            onClick={() => setDisconnecting(true)}
          >
            Disconnect
          </Button>
        )}
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        Reads what this GitLab user can see. A dedicated GitLab user with
        Reporter access is safest.
      </p>
      {c.version === "" ? (
        <p className="text-xs leading-relaxed text-weld-ink">
          Loomplane couldn&apos;t read this GitLab&apos;s version
        </p>
      ) : (
        !c.versionSupported && (
          <p className="text-xs leading-relaxed text-weld-ink">
            GitLab {c.version} is older than Loomplane supports; some things may
            not work
          </p>
        )
      )}
      {/* Mounted only while open, so closing it drops a secret it showed. */}
      {webhook && mayConnect && <GitLabWebhook connection={c} />}
      <DisconnectGitLab
        connection={c}
        open={disconnecting}
        onOpenChange={setDisconnecting}
      />
    </li>
  )
}

function DisconnectGitLab({
  connection,
  open,
  onOpenChange,
}: {
  connection: GitLabConnectionView
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const remove = useMutation(useGitlabDisconnect)
  const invalidate = useInvalidate()

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: connection.id } })
      toast.success(`Disconnected ${connection.username}`)
    } catch (err) {
      toast.error(messageOf(err, "It wasn't disconnected."))
    } finally {
      onOpenChange(false)
      invalidate(useGitlabStatus)
      invalidate(useGitlabRepositories)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Disconnect {connection.username} on {connection.host}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane stops reading the projects this user can see. Analyses
            already done stay.
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

/* ------------------------------------------------------------------------ */
/* Bitbucket                                                                  */

const bitbucketGuide =
  "https://loomplane.dev/docs/guides/bitbucket-repositories"

function BitbucketPanel() {
  const { can } = useOrganization()
  const status = useQuery(useBitbucketStatus)
  const { signIn } = useBitbucketSignIn()
  const [adding, setAdding] = useState(false)
  // The workspace whose sign-in is being asked for, so only its row spins.
  const [reconnecting, setReconnecting] = useState<string | null>(null)
  const data = status.data
  const workspaces = data?.workspaces ?? []
  const problems = data?.problems ?? []
  // The server says whether this person may connect, and their role must too.
  const mayConnect = !!data?.canConnect && can("repository:connect")
  // Without a consumer there is nothing to approve, so no sign-in can start.
  const maySignIn = mayConnect && !!data?.configured
  const connect = (
    <Button
      variant="outline"
      size={workspaces.length ? "sm" : "default"}
      onClick={() => setAdding(true)}
    >
      Connect workspace
    </Button>
  )

  return (
    <Panel
      title="Bitbucket"
      media={
        <span className="flex size-[30px] shrink-0 items-center justify-center rounded-lg bg-foreground text-background">
          <BitbucketMark className="size-4" />
        </span>
      }
      action={
        workspaces.length > 0 && data?.configured && mayConnect && connect
      }
    >
      {status.status === "error" ? (
        <div className="p-4">
          <QueryError
            error={status.error}
            fallback="The Bitbucket connection couldn't be loaded."
            retry={status.refetch}
          />
        </div>
      ) : !data ? (
        <div className="p-4">
          <Skeleton className="h-32 w-full" />
        </div>
      ) : (
        <>
          {problems.length > 0 && (
            <div className="grid gap-2 border-b p-4">
              {problems.map((p) => (
                <Alert key={p} variant="destructive">
                  <AlertDescription>{p}</AlertDescription>
                </Alert>
              ))}
            </div>
          )}
          {!data.configured && (
            <p
              className={cn(
                "px-4 py-4 text-[13px] leading-relaxed text-muted-foreground",
                workspaces.length > 0 && "border-b"
              )}
            >
              Only public repositories: no Bitbucket OAuth consumer is set up.
              Ask your operator to create one to read private ones. The{" "}
              <a
                href={bitbucketGuide}
                target="_blank"
                rel="noreferrer"
                className="font-medium text-brand-ink hover:underline"
              >
                Bitbucket guide
              </a>{" "}
              has the steps.
            </p>
          )}
          {workspaces.length === 0 ? (
            data.configured && (
              <Empty variant="inline">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <BitbucketMark className="size-4" />
                  </EmptyMedia>
                  <EmptyTitle>No Bitbucket workspaces yet</EmptyTitle>
                  <EmptyDescription>
                    {mayConnect
                      ? "Connect a workspace to let Loomplane read the private repositories you can see there."
                      : "An admin can connect a workspace."}
                  </EmptyDescription>
                </EmptyHeader>
                {mayConnect && <EmptyContent>{connect}</EmptyContent>}
              </Empty>
            )
          ) : (
            <ul className="divide-y">
              {workspaces.map((w) => (
                <BitbucketWorkspaceRow
                  key={w.id}
                  workspace={w}
                  mayConnect={mayConnect}
                  maySignIn={maySignIn}
                  onReconnect={async () => {
                    setReconnecting(w.workspace)
                    const failure = await signIn(w.workspace)
                    if (failure) {
                      setReconnecting(null)
                      toast.error(failure)
                    }
                  }}
                  reconnecting={reconnecting === w.workspace}
                  busy={reconnecting !== null}
                />
              ))}
            </ul>
          )}
        </>
      )}
      <BitbucketWorkspaceDialog open={adding} onOpenChange={setAdding} />
    </Panel>
  )
}

function BitbucketWorkspaceRow({
  workspace: w,
  mayConnect,
  maySignIn,
  onReconnect,
  reconnecting,
  busy,
}: {
  workspace: BitbucketWorkspaceView
  mayConnect: boolean
  /** Whether a sign-in can start: allowed, and a consumer is set up. */
  maySignIn: boolean
  onReconnect: () => Promise<void>
  /** This row's sign-in is being asked for. */
  reconnecting: boolean
  /** Some row's is, so no other starts meanwhile. */
  busy: boolean
}) {
  const [disconnecting, setDisconnecting] = useState(false)
  const name = w.name || w.workspace
  const lapsed = w.status === "reconnect_needed"
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
        <AccountAvatar account={name} src={w.avatarUrl} />
        <span className="grid min-w-0 flex-[1_1_160px] gap-0.5">
          <span className="truncate text-[13.5px] font-medium">{name}</span>
          <span className="text-xs text-muted-foreground">
            <span className="font-mono">{w.workspace}</span> · Connected by{" "}
            {w.connectedBy} on{" "}
            <time dateTime={w.createdAt}>{formatDate(w.createdAt)}</time>
          </span>
        </span>
        {lapsed ? (
          <StatusPill tone="waiting">Reconnect needed</StatusPill>
        ) : (
          <StatusPill tone="live">Connected</StatusPill>
        )}
        {lapsed && maySignIn && (
          <Button
            size="sm"
            aria-label={`Reconnect ${name}`}
            disabled={busy}
            onClick={() => void onReconnect()}
          >
            {reconnecting && <Spinner />}
            Reconnect
          </Button>
        )}
        {w.manageUrl && (
          <a
            href={w.manageUrl}
            target="_blank"
            rel="noreferrer"
            aria-label={`Manage ${name} on Bitbucket`}
            className={buttonVariants({ variant: "outline", size: "sm" })}
          >
            Manage
          </a>
        )}
        {mayConnect && (
          <Button
            variant="outline"
            size="sm"
            className="text-destructive"
            aria-label={`Disconnect ${name}`}
            onClick={() => setDisconnecting(true)}
          >
            Disconnect
          </Button>
        )}
      </div>
      {lapsed && w.message && (
        <p className="text-xs leading-relaxed text-weld-ink">{w.message}</p>
      )}
      <DisconnectBitbucket
        workspace={w}
        open={disconnecting}
        onOpenChange={setDisconnecting}
      />
    </li>
  )
}

function DisconnectBitbucket({
  workspace,
  open,
  onOpenChange,
}: {
  workspace: BitbucketWorkspaceView
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const remove = useMutation(useBitbucketDisconnect)
  const invalidate = useInvalidate()
  const name = workspace.name || workspace.workspace

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: workspace.id } })
      toast.success(`Disconnected ${name}`)
    } catch (err) {
      toast.error(messageOf(err, "It wasn't disconnected."))
    } finally {
      onOpenChange(false)
      invalidate(useBitbucketStatus)
      invalidate(useBitbucketRepositories)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Disconnect {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane stops reading this workspace&apos;s private repositories
            and takes its push webhooks off. Analyses already done stay. The
            consumer stays approved on Bitbucket until you revoke it there.
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
