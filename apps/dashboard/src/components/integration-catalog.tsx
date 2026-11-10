// Everything Loomplane can connect to, grouped by what it's for: where code
// comes from, the clouds and servers it deploys to, and where results go.
// The clouds and servers come from the API's own list of connection forms.
import { MailIcon, SearchIcon, ServerIcon, WebhookIcon } from "lucide-react"
import { useState, type ReactNode } from "react"

import {
  useBitbucketStatus,
  useConnectionCloudsList,
  useConnectionsList,
  useGithubStatus,
  useGitlabStatus,
  useNodesList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { BitbucketMark } from "@/components/bitbucket-mark"
import { CloudGlyph } from "@/components/cloud-glyph"
import { GitLabMark } from "@/components/gitlab-mark"
import { AddNode } from "@/components/nodes/add-node"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudPurposes, serverKinds } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

type Group = "source" | "cloud" | "server" | "notify"

const groups: { value: Group | "all"; label: string; note?: string }[] = [
  { value: "all", label: "All" },
  { value: "source", label: "Source code", note: "Where your code lives" },
  {
    value: "cloud",
    label: "Clouds",
    note: "Managed platforms Loomplane deploys to",
  },
  {
    value: "server",
    label: "Servers",
    note: "Your own machines, clusters and registries",
  },
  { value: "notify", label: "Notifications", note: "Where results go" },
]

const serverPurposes: Record<string, string> = {
  server:
    "Run services and catalog databases on your own machine over SSH, with retained volumes.",
  kubernetes:
    "Run services in a namespace you choose, with a retained storage class for databases.",
  registry: "Push the images Loomplane builds to a registry you own.",
}

type Card = {
  key: string
  group: Group
  name: string
  purpose: string
  icon: ReactNode
  connected: boolean
  detail: string
  action: ReactNode
}

export function IntegrationCatalog({
  onConnect,
  onManage,
}: {
  /** Opens the connect dialog for one cloud's form. */
  onConnect: (cloud: string) => void
  /**
   * Shows the connected accounts, such as GitHub's installations, and
   * where GitLab and Bitbucket sign-ins start.
   */
  onManage: () => void
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const forms = useQuery(useConnectionCloudsList)
  const connections = useQuery(useConnectionsList)
  const github = useQuery(useGithubStatus)
  const gitlab = useQuery(useGitlabStatus)
  const bitbucket = useQuery(useBitbucketStatus)
  const nodes = useQuery(useNodesList)
  const [addingNode, setAddingNode] = useState(false)
  const [group, setGroup] = useState<Group | "all">("all")
  const [query, setQuery] = useState("")
  const [onlyOn, setOnlyOn] = useState(false)

  if (forms.status === "error") {
    return (
      <QueryError
        error={forms.error}
        fallback="The list of integrations couldn't be loaded."
        retry={forms.refetch}
      />
    )
  }
  if (!forms.data || !connections.data) {
    return <Skeleton className="h-72 rounded-xl" />
  }

  const accounts = connections.data.connections
  const installs = github.data?.installations.length ?? 0
  const mayConnectCode = can("repository:connect")
  const gitlabAccounts = gitlab.data?.connections ?? []
  const gitlabLapsed = gitlabAccounts.filter((c) => c.lapsed).length
  // With no host and no way to add one, only public projects can be read.
  const gitlabSetUp =
    !!gitlab.data &&
    (gitlab.data.hosts.length > 0 || gitlab.data.organizationHosts)
  const workspaces = bitbucket.data?.workspaces ?? []
  const workspacesLapsed = workspaces.filter(
    (w) => w.status === "reconnect_needed"
  ).length
  const nodeList = (nodes.data?.nodes ?? []).filter(
    (n) => n.status !== "revoked"
  )
  const cards: Card[] = [
    {
      key: "github",
      group: "source",
      name: "GitHub",
      purpose:
        "Reads repositories to find services. Private ones get a read-only token for one analysis, and Loomplane keeps none.",
      icon: <ProductIcon product="github" size="md" />,
      connected: installs > 0,
      detail: installs
        ? `${installs} ${installs === 1 ? "account" : "accounts"}`
        : github.data?.configured === false
          ? "Needs setting up"
          : "Not installed",
      action:
        installs > 0 ? (
          <Button size="sm" variant="outline" onClick={onManage}>
            Manage
          </Button>
        ) : github.data?.installUrl ? (
          <Button
            size="sm"
            nativeButton={false}
            render={<a href={github.data.installUrl} />}
          >
            Install
          </Button>
        ) : (
          <Button size="sm" variant="outline" onClick={onManage}>
            Set up
          </Button>
        ),
    },
    {
      key: "gitlab",
      group: "source",
      name: "GitLab",
      purpose:
        "Reads projects on gitlab.com or a self-managed GitLab with read-only access, and analyzes them again when you push.",
      icon: <ProductIcon fallback={GitLabMark} label="GitLab" size="md" />,
      connected: gitlabAccounts.length > 0,
      detail: gitlabAccounts.length
        ? gitlabLapsed
          ? `${gitlabLapsed} of ${gitlabAccounts.length} need signing in`
          : `${gitlabAccounts.length} ${gitlabAccounts.length === 1 ? "account" : "accounts"}`
        : gitlab.data && !gitlabSetUp
          ? "Public projects only"
          : "No accounts yet",
      action: (
        <Button
          size="sm"
          variant={
            gitlabAccounts.length === 0 &&
            gitlabSetUp &&
            gitlab.data?.canConnect &&
            mayConnectCode
              ? "default"
              : "outline"
          }
          onClick={onManage}
        >
          {gitlabAccounts.length
            ? "Manage"
            : gitlabSetUp && gitlab.data?.canConnect && mayConnectCode
              ? "Connect"
              : "Set up"}
        </Button>
      ),
    },
    {
      key: "bitbucket",
      group: "source",
      name: "Bitbucket",
      purpose:
        "Reads repositories in the Bitbucket workspaces you connect, with read-only access.",
      icon: (
        <ProductIcon fallback={BitbucketMark} label="Bitbucket" size="md" />
      ),
      connected: workspaces.length > 0,
      detail: workspaces.length
        ? workspacesLapsed
          ? `${workspacesLapsed} of ${workspaces.length} need reconnecting`
          : `${workspaces.length} ${workspaces.length === 1 ? "workspace" : "workspaces"}`
        : bitbucket.data?.configured === false
          ? "Needs setting up"
          : "No workspaces yet",
      action: (
        <Button
          size="sm"
          variant={
            workspaces.length === 0 &&
            bitbucket.data?.configured &&
            bitbucket.data.canConnect &&
            mayConnectCode
              ? "default"
              : "outline"
          }
          onClick={onManage}
        >
          {workspaces.length
            ? "Manage"
            : bitbucket.data?.configured &&
                bitbucket.data.canConnect &&
                mayConnectCode
              ? "Connect"
              : "Set up"}
        </Button>
      ),
    },
    ...forms.data.clouds.map((f): Card => {
      const mine = accounts.filter((c) => c.cloud === f.cloud)
      const ready = mine.filter((c) => c.status === "ready").length
      return {
        key: f.cloud,
        group: serverKinds.has(f.cloud) ? "server" : "cloud",
        name: f.title,
        purpose:
          cloudPurposes[f.cloud] ??
          serverPurposes[f.cloud] ??
          f.help ??
          "Deploy services to it.",
        icon: <CloudGlyph cloud={f.cloud} size="md" />,
        connected: mine.length > 0,
        detail: mine.length
          ? `${ready} of ${mine.length} ready`
          : manage
            ? "No accounts yet"
            : "An admin can connect it",
        action: manage && (
          <Button
            size="sm"
            variant={mine.length ? "outline" : "default"}
            onClick={() => onConnect(f.cloud)}
          >
            {mine.length ? "Add another" : "Connect"}
          </Button>
        ),
      }
    }),
    {
      key: "node",
      group: "server",
      name: "Add node",
      purpose: "Join any Linux machine. No open ports.",
      icon: <ProductIcon fallback={ServerIcon} label="Node" size="md" />,
      connected: nodeList.some((n) => n.status === "online"),
      detail: nodeList.length
        ? `${nodeList.filter((n) => n.status === "online").length} of ${nodeList.length} online`
        : "No nodes yet",
      action: can("nodes:manage") ? (
        <Button
          size="sm"
          variant={nodeList.length ? "outline" : "default"}
          onClick={() => setAddingNode(true)}
        >
          Join a machine
        </Button>
      ) : (
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={<Link to="/nodes" />}
        >
          View nodes
        </Button>
      ),
    },
    {
      key: "webhooks",
      group: "notify",
      name: "Webhooks",
      purpose:
        "Signed deployment results sent to a public HTTPS endpoint, with retries.",
      icon: <ProductIcon fallback={WebhookIcon} label="Webhooks" size="md" />,
      connected: false,
      detail: "Workspace-wide",
      action: (
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={<Link to="/notifications" />}
        >
          Manage
        </Button>
      ),
    },
    {
      key: "email",
      group: "notify",
      name: "Email",
      purpose: "Results for deployments you started, sent to your inbox.",
      icon: <ProductIcon fallback={MailIcon} label="Email" size="md" />,
      connected: false,
      detail: "Per person",
      action: (
        <Button
          size="sm"
          variant="outline"
          nativeButton={false}
          render={<Link to="/notifications" />}
        >
          Manage
        </Button>
      ),
    },
  ]

  const q = query.trim().toLowerCase()
  const base = cards.filter(
    (c) =>
      (!q || `${c.name} ${c.purpose}`.toLowerCase().includes(q)) &&
      (!onlyOn || c.connected)
  )
  const visible = groups.filter(
    (g): g is (typeof groups)[number] & { value: Group } =>
      g.value !== "all" && (group === "all" || group === g.value)
  )

  return (
    <div className="grid gap-5">
      <div className="flex flex-wrap items-center gap-2">
        <label className="relative flex min-w-[220px] flex-[1_1_260px] items-center">
          <SearchIcon className="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
          <span className="sr-only">Search integrations</span>
          <input
            data-page-search
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search integrations"
            className="h-9 w-full rounded-[10px] border border-transparent bg-muted pr-3 pl-9 text-[13.5px] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
          />
        </label>
        <div
          role="radiogroup"
          aria-label="Category"
          className="flex flex-wrap gap-0.5 rounded-[10px] bg-muted p-[3px]"
        >
          {groups.map((g) => {
            const on = g.value === group
            const n = base.filter(
              (c) => g.value === "all" || c.group === g.value
            ).length
            return (
              <button
                key={g.value}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => setGroup(g.value)}
                className={cn(
                  "flex h-[30px] items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium",
                  on
                    ? "bg-background text-foreground shadow-xs"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {g.label}
                <span className="font-mono text-[11px] text-muted-foreground">
                  {n}
                </span>
              </button>
            )
          })}
        </div>
        <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
          <input
            type="checkbox"
            checked={onlyOn}
            onChange={() => setOnlyOn(!onlyOn)}
            className="size-4 accent-[var(--primary)]"
          />
          Connected only
        </label>
      </div>

      {visible.every((g) => !base.some((c) => c.group === g.value)) ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>No integrations match</EmptyTitle>
            <EmptyDescription>
              Try another name, or turn off Connected only.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        visible.map((g) => {
          const items = base.filter((c) => c.group === g.value)
          if (items.length === 0) return null
          return (
            <section key={g.value} className="grid gap-2.5">
              <div className="flex items-baseline gap-2">
                <h2 className="text-sm font-semibold">{g.label}</h2>
                <span className="text-[12.5px] text-muted-foreground">
                  {g.note}
                </span>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {items.map((c) => (
                  <article
                    key={c.key}
                    className="flex flex-col gap-2.5 rounded-xl bg-card p-4 ring-1 ring-border"
                  >
                    <div className="flex items-center gap-2.5">
                      {c.icon}
                      <span className="min-w-0 flex-1 truncate text-[14.5px] font-semibold">
                        {c.name}
                      </span>
                      <StatusPill tone={c.connected ? "live" : "quiet"}>
                        {c.connected ? "Connected" : "Not connected"}
                      </StatusPill>
                    </div>
                    {/* Two lines, always, so every card in the catalog is the
                        same height; the full text is on hover. */}
                    <p
                      title={c.purpose}
                      className="line-clamp-2 min-h-[2lh] flex-1 text-[12.5px] leading-relaxed text-muted-foreground"
                    >
                      {c.purpose}
                    </p>
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-xs text-muted-foreground">
                        {c.detail}
                      </span>
                      {c.action}
                    </div>
                  </article>
                ))}
              </div>
            </section>
          )
        })
      )}
      {can("nodes:manage") && (
        <AddNode open={addingNode} onOpenChange={setAddingNode} />
      )}
    </div>
  )
}
