// Integrations, laid out like the overview: what's connected at a glance,
// then the GitHub accounts Loomplane reads code through, the repositories
// they let it read, and a tile for each cloud it deploys to.
import {
  BookMarkedIcon,
  CloudIcon,
  EllipsisIcon,
  ExternalLinkIcon,
  FolderGit2Icon,
  GitBranchIcon,
  LockIcon,
  PlugIcon,
  PlusIcon,
  SearchIcon,
} from "lucide-react"
import { type ReactNode, useMemo, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionsList,
  useGithubDisconnect,
  useGithubRepositories,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import type {
  ConnectedRepository,
  GitHubInstallation,
  GitHubStatus,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudConnections, ConnectDialog } from "@/components/cloud-connections"
import { DotProgress } from "@/components/dots"
import { Figure, MetricCard, QuickAction } from "@/components/metric-card"
import { NewProjectDialog } from "@/components/new-project-dialog"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { formatDate } from "@/lib/format"
import { cloudHealth, groupRepositories } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

export function Integrations() {
  const [connecting, setConnecting] = useState(false)
  // The repository a new project starts with, while its dialog is open.
  const [starting, setStarting] = useState<string | null>(null)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Integrations" }]} />
      <Page className="gap-6">
        <PageHeading
          title="Integrations"
          description="Where Loomplane reads your code from, and the clouds it deploys to."
        />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-12">
          <Summary
            className="sm:col-span-2 lg:col-span-6"
            onConnectCloud={() => setConnecting(true)}
            onNewProject={() => setStarting("")}
          />
          <RepositoryFigure className="lg:col-span-3" />
          <CloudFigure className="lg:col-span-3" />
          <GitHubCard className="sm:col-span-2 lg:col-span-7" />
          <RepositoriesCard
            className="sm:col-span-2 lg:col-span-5"
            onStart={setStarting}
          />
          <CloudConnections className="sm:col-span-2 lg:col-span-12" />
        </div>
      </Page>
      <ConnectDialog open={connecting} onOpenChange={setConnecting} />
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

function Summary({
  className,
  onConnectCloud,
  onNewProject,
}: {
  className?: string
  onConnectCloud: () => void
  onNewProject: () => void
}) {
  const { can } = useOrganization()
  const github = useQuery(useGithubStatus)
  const clouds = useQuery(useConnectionsList)
  const accounts = github.data?.installations.length ?? 0
  const connected = clouds.data?.connections.length ?? 0
  const total = accounts + connected
  const loading = !github.data || !clouds.data

  return (
    <Card className={cn("hero-glow isolate", className)}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-[15px] font-normal text-muted-foreground">
          <PlugIcon className="size-4" />
          Connected
        </CardTitle>
        <CardDescription className="grid gap-2 pt-1">
          {loading ? (
            <Skeleton className="h-11 w-48" />
          ) : (
            <span className="flex items-baseline gap-2 text-foreground">
              <span className="text-[44px] leading-none font-semibold tracking-[-0.03em] tabular-nums">
                {total}
              </span>
              <span className="text-2xl font-medium tracking-tight text-muted-foreground">
                {total === 1 ? "account" : "accounts"}
              </span>
            </span>
          )}
          {!loading && summaryLine(github.data, accounts, connected)}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid grid-cols-3 gap-2 sm:flex sm:flex-wrap">
        <QuickAction
          icon={GitHubMark}
          label="Install on GitHub"
          href={github.data?.installUrl}
          disabled={!github.data?.installUrl}
        />
        <QuickAction
          icon={CloudIcon}
          label="Connect a cloud"
          disabled={!can("cloud_account:manage")}
          onClick={onConnectCloud}
        />
        <QuickAction
          icon={PlusIcon}
          label="New project"
          disabled={!can("project:create")}
          onClick={onNewProject}
        />
      </CardContent>
      <CardFooter className="border-t-0 bg-transparent text-xs text-muted-foreground">
        Public repositories need nothing here. For a private one, Loomplane gets
        a read-only token for that one repository, for as long as an analysis
        takes, and keeps none.
      </CardFooter>
    </Card>
  )
}

function summaryLine(
  github: GitHubStatus | undefined,
  accounts: number,
  clouds: number
): string {
  if (accounts + clouds === 0) {
    return github?.configured
      ? "Nothing connected yet. Install the GitHub App on an organization, then connect a cloud to deploy to."
      : "Nothing connected yet. Connect a cloud to deploy to."
  }
  const parts = []
  if (accounts > 0) parts.push(`${accounts} on GitHub`)
  if (clouds > 0) parts.push(`${clouds} ${clouds === 1 ? "cloud" : "clouds"}`)
  return parts.join(" and ")
}

function RepositoryFigure({ className }: { className?: string }) {
  const github = useQuery(useGithubStatus)
  const installations = github.data?.installations ?? []

  return (
    <MetricCard
      icon={BookMarkedIcon}
      title="Repositories"
      className={className}
    >
      {!github.data ? (
        <Skeleton className="h-12 w-32" />
      ) : installations.length > 0 ? (
        <RepositoryCount accounts={installations.length} />
      ) : (
        <Figure
          value={<span className="text-muted-foreground">Public</span>}
          note={
            github.data.configured
              ? "Only public ones, until an account is connected"
              : "Only public ones: no GitHub App is set up"
          }
        />
      )}
    </MetricCard>
  )
}

// RepositoryCount asks GitHub, so it's only shown once an account is
// connected.
function RepositoryCount({ accounts }: { accounts: number }) {
  const repos = useQuery(useGithubRepositories)
  if (repos.status === "error") {
    return (
      <Figure
        value={<span className="text-muted-foreground">?</span>}
        note="GitHub didn't answer"
      />
    )
  }
  if (!repos.data) return <Skeleton className="h-12 w-32" />
  const list = repos.data.repositories
  const hidden = list.filter((r) => r.private).length
  return (
    <Figure
      value={list.length}
      unit="Loomplane can read"
      note={`${hidden} private, across ${accounts} ${accounts === 1 ? "account" : "accounts"}`}
    />
  )
}

function CloudFigure({ className }: { className?: string }) {
  const list = useQuery(useConnectionsList)
  const health = cloudHealth(list.data?.connections ?? [])

  return (
    <MetricCard icon={CloudIcon} title="Clouds ready" className={className}>
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="Cloud connections couldn't be loaded."
          retry={list.refetch}
        />
      ) : !list.data ? (
        <Skeleton className="h-12 w-32" />
      ) : (
        <>
          <Figure
            value={health.ready}
            unit={health.total > 0 ? `of ${health.total}` : undefined}
            note={
              health.total === 0 ? (
                "None connected yet"
              ) : health.attention > 0 ? (
                <span className="text-weld-ink">
                  {health.attention} {health.attention === 1 ? "needs" : "need"}{" "}
                  attention
                </span>
              ) : (
                "All checked and ready"
              )
            }
          />
          <DotProgress
            size="sm"
            className="mt-auto"
            value={health.total > 0 ? health.ready / health.total : 0}
            label={`${health.ready} of ${health.total} clouds ready`}
          />
        </>
      )}
    </MetricCard>
  )
}

/* ------------------------------------------------------------------------ */
/* GitHub                                                                     */

function GitHubCard({ className }: { className?: string }) {
  const { can } = useOrganization()
  const status = useQuery(useGithubStatus)
  const has = (status.data?.installations.length ?? 0) > 0
  const mayConnect = can("repository:connect")

  return (
    <MetricCard
      icon={GitHubMark}
      title="GitHub"
      className={className}
      action={
        has && status.data?.installUrl ? (
          // A link, not a button: it leaves for GitHub.
          <a
            href={status.data.installUrl}
            className={cn(
              buttonVariants({ variant: "outline", size: "sm" }),
              "-mt-1 rounded-full"
            )}
          >
            <PlusIcon />
            Install on another account
          </a>
        ) : undefined
      }
    >
      {status.status === "error" && (
        <Alert variant="destructive">
          <AlertDescription>
            {messageOf(
              status.error,
              "The GitHub connection couldn't be loaded."
            )}
          </AlertDescription>
        </Alert>
      )}
      {!status.data && status.status !== "error" && (
        <Skeleton className="h-40 w-full rounded-xl" />
      )}
      {status.data && !status.data.configured && <SetUpGitHub />}
      {status.data?.configured && !has && (
        <Empty className="flex-1 border border-dashed">
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
              <a
                href={status.data.installUrl}
                className={cn(buttonVariants(), "rounded-full")}
              >
                <GitHubMark className="size-4" />
                Install on GitHub
              </a>
            </EmptyContent>
          )}
        </Empty>
      )}
      {status.data && has && (
        <Installations
          installations={status.data.installations}
          mayConnect={mayConnect}
        />
      )}
    </MetricCard>
  )
}

// Installations asks GitHub how many repositories each lets Loomplane
// read, so it's only shown once an account is connected.
function Installations({
  installations,
  mayConnect,
}: {
  installations: GitHubInstallation[]
  mayConnect: boolean
}) {
  const repos = useQuery(useGithubRepositories)
  const counts = useMemo(() => {
    const m = new Map<string, number>()
    for (const r of repos.data?.repositories ?? []) {
      m.set(r.installation, (m.get(r.installation) ?? 0) + 1)
    }
    return m
  }, [repos.data])
  return (
    <ItemGroup className="gap-2">
      {installations.map((inst) => (
        <Installation
          key={inst.id}
          installation={inst}
          repositories={repos.data ? (counts.get(inst.id) ?? 0) : undefined}
          mayConnect={mayConnect}
        />
      ))}
    </ItemGroup>
  )
}

function Installation({
  installation: inst,
  repositories,
  mayConnect,
}: {
  installation: GitHubInstallation
  repositories?: number
  mayConnect: boolean
}) {
  const [disconnecting, setDisconnecting] = useState(false)
  const kind = inst.accountType === "User" ? "Personal" : "Organization"

  return (
    <Item variant="outline" className="rounded-xl bg-background/50">
      <ItemMedia>
        <AccountAvatar account={inst.account} src={inst.avatarUrl} />
      </ItemMedia>
      <ItemContent>
        <ItemTitle>
          {inst.account}
          <Badge variant="outline" className="font-normal">
            {kind}
          </Badge>
        </ItemTitle>
        <ItemDescription className="text-xs">
          {repositories === undefined
            ? "Connected"
            : `${repositories} ${repositories === 1 ? "repository" : "repositories"}, connected`}{" "}
          <time dateTime={inst.createdAt}>{formatDate(inst.createdAt)}</time>
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        {inst.manageUrl && (
          <a
            href={inst.manageUrl}
            target="_blank"
            rel="noreferrer"
            className={cn(
              buttonVariants({ variant: "ghost", size: "sm" }),
              "rounded-full"
            )}
          >
            Choose repositories
            <ExternalLinkIcon />
          </a>
        )}
        {mayConnect && (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="rounded-full text-muted-foreground"
                  aria-label={`${inst.account} options`}
                />
              }
            >
              <EllipsisIcon />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-44">
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setDisconnecting(true)}
              >
                Disconnect
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </ItemActions>
      <Disconnect
        installation={inst}
        open={disconnecting}
        onOpenChange={setDisconnecting}
      />
    </Item>
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

/* ------------------------------------------------------------------------ */
/* Repositories                                                               */

function RepositoriesCard({
  className,
  onStart,
}: {
  className?: string
  onStart: (repository: string) => void
}) {
  const status = useQuery(useGithubStatus)
  const installations = status.data?.installations ?? []

  return (
    <MetricCard
      icon={FolderGit2Icon}
      title="Repositories"
      className={className}
    >
      {status.status === "error" ? (
        <QueryError
          error={status.error}
          fallback="GitHub couldn't be loaded."
          retry={status.refetch}
        />
      ) : !status.data ? (
        <Skeleton className="h-40 w-full" />
      ) : installations.length > 0 ? (
        <RepositoryBrowser installations={installations} onStart={onStart} />
      ) : (
        <Empty className="p-0">
          <EmptyHeader>
            <EmptyTitle>No repositories connected</EmptyTitle>
            <EmptyDescription>
              {status.data.configured
                ? "Install the App on an account to see the repositories it lets Loomplane read."
                : "Once the GitHub App is set up, the repositories it can read are listed here."}
            </EmptyDescription>
          </EmptyHeader>
          {status.data.installUrl && (
            <EmptyContent>
              <a
                href={status.data.installUrl}
                className={buttonVariants({ variant: "outline", size: "sm" })}
              >
                Install on GitHub
              </a>
            </EmptyContent>
          )}
        </Empty>
      )}
    </MetricCard>
  )
}

function RepositoryBrowser({
  installations,
  onStart,
}: {
  installations: GitHubInstallation[]
  onStart: (repository: string) => void
}) {
  const { can } = useOrganization()
  const repos = useQuery(useGithubRepositories)
  const [query, setQuery] = useState("")
  const all = useMemo(() => repos.data?.repositories ?? [], [repos.data])
  const groups = useMemo(
    () => groupRepositories(all, installations, query),
    [all, installations, query]
  )
  const mayCreate = can("project:create")

  if (repos.status === "error") {
    return (
      <QueryError
        error={repos.error}
        fallback="The repositories couldn't be listed."
        retry={repos.refetch}
      />
    )
  }
  if (!repos.data) {
    return (
      <div className="grid gap-2">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }
  if (all.length === 0) {
    return (
      <Empty className="p-0">
        <EmptyHeader>
          <EmptyTitle>No repositories available</EmptyTitle>
          <EmptyDescription>
            Choose repositories in the App&apos;s settings on GitHub.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          {installations
            .filter((i) => i.manageUrl)
            .map((i) => (
              <a
                key={i.id}
                href={i.manageUrl}
                className="text-sm underline underline-offset-4"
              >
                Manage {i.account}
              </a>
            ))}
        </EmptyContent>
      </Empty>
    )
  }
  return (
    <>
      <InputGroup>
        <InputGroupAddon>
          <SearchIcon />
        </InputGroupAddon>
        <InputGroupInput
          placeholder={`Search ${all.length} ${all.length === 1 ? "repository" : "repositories"}`}
          aria-label="Search repositories"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </InputGroup>
      <div className="-mx-3 grid max-h-[26rem] content-start gap-4 overflow-y-auto px-3">
        {groups.length === 0 && (
          <Empty className="p-0">
            <EmptyHeader>
              <EmptyTitle>No repositories match</EmptyTitle>
              <EmptyDescription>
                No repository matches &ldquo;{query.trim()}&rdquo;.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button variant="outline" size="sm" onClick={() => setQuery("")}>
                Clear search
              </Button>
            </EmptyContent>
          </Empty>
        )}
        {groups.map((g) => (
          <section key={g.key} className="grid gap-1">
            <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
              <AccountAvatar
                account={g.account}
                src={g.avatarUrl}
                className="size-4 rounded-[4px] after:rounded-[4px]"
              />
              {g.account}
              <span className="tabular-nums">{g.repositories.length}</span>
            </h3>
            <ItemGroup className="gap-0.5">
              {g.repositories.map((r) => (
                <RepositoryRow
                  key={r.fullName}
                  repository={r}
                  onStart={mayCreate ? onStart : undefined}
                />
              ))}
            </ItemGroup>
          </section>
        ))}
      </div>
    </>
  )
}

function RepositoryRow({
  repository: r,
  onStart,
}: {
  repository: ConnectedRepository
  onStart?: (repository: string) => void
}) {
  const name = r.fullName.split("/").slice(1).join("/") || r.fullName
  return (
    <Item size="sm" className="group/repo -mx-3">
      <ItemContent>
        <ItemTitle className="font-mono text-[13px]">
          {name}
          {r.private && (
            <LockIcon
              className="size-3 text-muted-foreground"
              aria-label="Private"
            />
          )}
        </ItemTitle>
        <ItemDescription className="flex items-center gap-1 text-xs">
          <GitBranchIcon className="size-3" aria-hidden="true" />
          {r.defaultBranch || "main"}
        </ItemDescription>
      </ItemContent>
      <ItemActions className="gap-0.5">
        {onStart && (
          <Button
            variant="ghost"
            size="sm"
            className="rounded-full"
            onClick={() => onStart(r.fullName)}
          >
            <PlusIcon />
            Project
          </Button>
        )}
        <a
          href={r.url}
          target="_blank"
          rel="noreferrer"
          aria-label={`${r.fullName} on GitHub`}
          className={cn(
            buttonVariants({ variant: "ghost", size: "icon-sm" }),
            "rounded-full text-muted-foreground"
          )}
        >
          <ExternalLinkIcon />
        </a>
      </ItemActions>
    </Item>
  )
}
