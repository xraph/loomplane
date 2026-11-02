// One connected account: whether Loomplane can still use it, what it can
// run, what Loomplane runs on it now, and replacing or removing it.
import { DatabaseIcon, GlobeIcon } from "lucide-react"
import { useState, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionCloudsList,
  useConnectionsCheck,
  useConnectionsDelete,
  useConnectionsGet,
  useConnectionsList,
  useMembersList,
  useProjectsList,
  useUsageSpend,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  Connection,
  Project,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import {
  ConnectDialog,
  needsSignInAgain,
  statusLabels,
  useSignInAgain,
} from "@/components/cloud-connections"
import {
  Cell,
  CloudCallout,
  CloudConfirm,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { GpuInventory } from "@/components/gpu-inventory"
import { CloudsIcon } from "@/components/icons"
import { Page } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { EnvBadge, StatusPill, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { cloudLabel } from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { cloudPurposes } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { money } from "@/lib/usage"
import { usePeople, useRunning } from "@/lib/use-runs"

const tone: Record<string, Tone> = {
  ready: "live",
  incomplete: "waiting",
  failed: "failed",
  sign_in_needed: "waiting",
}

// What a not-ready account needs, as the callout's title.
const notReady: Record<string, string> = {
  incomplete: "Some permissions are missing",
  failed: "The provider refused this credential",
  sign_in_needed: "Sign in again to keep deploying here",
}

/** credentialLabel says how Loomplane reaches the account. */
function credentialLabel(c: Connection): string {
  switch (c.method) {
    case "keyless":
      return "Keyless, no stored key"
    case "oauth":
      return "Sign-in"
    case "managed_kubernetes":
      return "Namespace access"
    case "in_cluster":
      return "In-cluster access"
    default:
      return "Stored key or token"
  }
}

export function CloudAccount({ id }: { id: string }) {
  const { can } = useOrganization()
  const person = usePeople()
  const connection = useQuery(useConnectionsGet, { path: { id } })
  const members = useQuery(useMembersList)
  const check = useMutation(useConnectionsCheck)
  const remove = useMutation(useConnectionsDelete)
  const invalidate = useInvalidate()
  const { signInAgain, pending: signingIn } = useSignInAgain()
  const running = useRunning()
  const projects = useQuery(useProjectsList)
  const [replacing, setReplacing] = useState(false)
  const [switching, setSwitching] = useState(false)
  const clouds = useQuery(useConnectionCloudsList)
  const [removing, setRemoving] = useState(false)
  const [removeError, setRemoveError] = useState("")
  const spend = useSpend(id)
  const c = connection.data
  const manage = can("cloud_account:manage")
  const here = running.bindings.filter((b) => b.connectionId === id)
  const services = here.filter((b) => b.subject === "service").length
  // The member's name when the members list has them, else their ID.
  const signedInAs = String(c?.details.signed_in_as ?? "")
  const who = members.data?.members.find((m) => m.userId === c?.signedInBy)
  const signedInBy = who
    ? who.name || who.email || c?.signedInBy
    : c?.signedInBy
  // Keyless is offered where the cloud has it and the connection isn't
  // there already.
  const canSwitch =
    c?.method !== "keyless" &&
    !!clouds.data?.clouds
      .find((f) => f.cloud === c?.cloud)
      ?.methods.includes("keyless")
  const missing =
    connection.status === "error" && statusOf(connection.error) === 404

  const recheck = async () => {
    if (!c) return
    try {
      const next = await check.mutateAsync({ path: { id: c.id } })
      void connection.refetch()
      toast.success(
        next.status === "ready"
          ? `${c.name} is ready`
          : `${c.name} is still ${statusLabels[next.status]?.toLowerCase() ?? next.status}`
      )
    } catch (err) {
      toast.error(messageOf(err, "The connection couldn't be checked."))
    }
  }

  const confirmRemove = async () => {
    if (!c) return
    setRemoveError("")
    try {
      await remove.mutateAsync({ path: { id: c.id } })
      invalidate(useConnectionsList)
      setRemoving(false)
      toast.success(`Removed ${c.name}`)
    } catch (err) {
      setRemoveError(messageOf(err, "The connection wasn't removed."))
    }
  }

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Clouds", to: "/clouds" },
          { label: c?.name ?? "Account" },
        ]}
      />
      <Page>
        {connection.status === "error" && !missing ? (
          <QueryError
            error={connection.error}
            fallback="The account couldn't be loaded."
            retry={connection.refetch}
          />
        ) : missing ? (
          <CloudEmpty
            kind="notice"
            icon={CloudsIcon}
            title="No connected account here"
            action={
              <Button nativeButton={false} render={<Link to="/clouds" />}>
                All Clouds
              </Button>
            }
          >
            It may have been removed, or it belongs to another workspace.
          </CloudEmpty>
        ) : !c ? (
          <div className="grid gap-4" aria-busy="true">
            <Skeleton className="h-12 w-80" />
            <Skeleton className="h-32 rounded-xl" />
          </div>
        ) : (
          <div className="grid gap-6">
            <CloudPageHeader
              glyph={<CloudGlyph cloud={c.cloud} size="xl" />}
              title={c.name}
              status={
                <StatusPill tone={tone[c.status] ?? "quiet"}>
                  {statusLabels[c.status] ?? c.status}
                </StatusPill>
              }
              meta={
                <>
                  Connected account · {cloudLabel(c.cloud)}
                  {c.account && (
                    <>
                      {" · "}
                      <code className="font-mono">{c.account}</code>
                    </>
                  )}
                  {" · connected "}
                  {formatDate(c.createdAt)} by {person(c.createdBy).name}
                  {c.method === "keyless" && (
                    <>
                      {" · "}
                      <span>Connected keylessly</span>
                    </>
                  )}
                  {signedInAs && (
                    <>
                      {" · "}
                      <span data-signed-in>
                        Signed in as {signedInAs}
                        {c.signedInBy && `, by ${signedInBy}`}
                      </span>
                    </>
                  )}
                </>
              }
              actions={
                manage && (
                  <>
                    {needsSignInAgain(c) ? (
                      <Button
                        variant="outline"
                        disabled={signingIn}
                        onClick={() => void signInAgain(c)}
                      >
                        {signingIn && <Spinner />}
                        Sign in again
                      </Button>
                    ) : (
                      <Button
                        variant="outline"
                        disabled={check.isPending}
                        onClick={() => void recheck()}
                      >
                        {check.isPending && <Spinner />}
                        Check again
                      </Button>
                    )}
                    <PageMenu label="More account actions">
                      {needsSignInAgain(c) && (
                        <DropdownMenuItem
                          disabled={check.isPending}
                          onClick={() => void recheck()}
                        >
                          Check again
                        </DropdownMenuItem>
                      )}
                      {c.method !== "managed_kubernetes" && (
                        <DropdownMenuItem onClick={() => setReplacing(true)}>
                          Replace credential
                        </DropdownMenuItem>
                      )}
                      {canSwitch && (
                        <DropdownMenuItem onClick={() => setSwitching(true)}>
                          Switch to keyless
                        </DropdownMenuItem>
                      )}
                      {can("infrastructure:read") && (
                        <DropdownMenuItem
                          render={
                            <Link to="/resources" search={{ connection: id }} />
                          }
                        >
                          Provider resources
                        </DropdownMenuItem>
                      )}
                      {c.method !== "managed_kubernetes" && (
                        <>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => setRemoving(true)}
                          >
                            Remove account
                          </DropdownMenuItem>
                        </>
                      )}
                    </PageMenu>
                  </>
                )
              }
            />

            {c.status !== "ready" && (
              <CloudCallout
                tone={c.status === "failed" ? "error" : "attention"}
                title={
                  notReady[c.status] ??
                  "Loomplane can't use this account for every deployment yet"
                }
              >
                {[
                  c.message,
                  c.missing.length > 0 && `Missing: ${c.missing.join(", ")}.`,
                ]
                  .filter(Boolean)
                  .join(" ") || undefined}
              </CloudCallout>
            )}

            <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)] lg:items-start">
              <section aria-label="What runs here" className="grid gap-2.5">
                <CloudSectionHead
                  title="What runs here"
                  count={
                    running.loaded || here.length > 0 ? here.length : undefined
                  }
                  description={`${services} ${services === 1 ? "service" : "services"} and ${here.length - services} ${here.length - services === 1 ? "resource" : "resources"} Loomplane deployed to this account.`}
                />
                {!running.loaded && here.length === 0 ? (
                  <Skeleton className="h-32 rounded-xl" />
                ) : here.length === 0 ? (
                  <CloudEmpty
                    kind="list"
                    icon={GlobeIcon}
                    title="No services or resources yet"
                  >
                    Nothing Loomplane deployed runs on this account yet.
                  </CloudEmpty>
                ) : (
                  <CloudTable
                    label="What runs here"
                    columns="minmax(0,1.3fr) minmax(0,1.2fr) 110px 150px"
                    head={["Name", "Environment", "Region", "Status"]}
                    minWidth={560}
                  >
                    {here.map((b) => (
                      <RunningRow
                        key={`${b.environmentId}:${b.subject}:${b.name}`}
                        binding={b}
                        project={projects.data?.projects.find(
                          (x) => x.id === b.projectId
                        )}
                        env={running.envNames.get(b.environmentId) ?? ""}
                      />
                    ))}
                  </CloudTable>
                )}
              </section>

              <section aria-label="About" className="grid gap-2.5">
                <CloudSectionHead
                  title="About"
                  description="What Loomplane can do with this account."
                />
                <FactList
                  facts={[
                    {
                      label: "Last checked",
                      value: c.checkedAt ? formatDate(c.checkedAt) : "Never",
                    },
                    {
                      label: "Permissions",
                      value:
                        c.method === "managed_kubernetes"
                          ? "Namespace worker access; HTTP, builds and storage are not established"
                          : c.status === "ready"
                            ? "Can do everything a deployment needs"
                            : (statusLabels[c.status] ?? c.status),
                    },
                    {
                      label: "Can run",
                      value:
                        c.method === "managed_kubernetes"
                          ? "Project-owned namespace workers"
                          : (cloudPurposes[c.cloud] ??
                            "Services Loomplane deploys"),
                    },
                    { label: "Credential", value: credentialLabel(c) },
                    { label: "Spend this month", value: spend },
                    {
                      label: "Account ID",
                      value: c.account ? (
                        <code className="font-mono">{c.account}</code>
                      ) : (
                        "Not reported"
                      ),
                    },
                  ]}
                />
              </section>
            </div>

            {c.method !== "managed_kubernetes" &&
              (c.cloud === "server" || c.cloud === "kubernetes") && (
                <GpuInventory connectionId={id} cloud={c.cloud} />
              )}

            {manage && c.method !== "managed_kubernetes" && (
              <section aria-label="Danger zone" className="grid gap-2.5">
                <CloudSectionHead
                  title="Danger zone"
                  description="Each asks you to confirm in a dialog before anything happens."
                />
                <div className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/30">
                  <div className="grid min-w-0 flex-[1_1_360px] gap-0.5">
                    <p className="text-[13px] font-semibold">
                      Remove this account
                    </p>
                    <p className="text-[12.5px] leading-relaxed text-muted-foreground">
                      What already runs here keeps running; Loomplane just stops
                      managing and deploying to it.
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    className="text-destructive"
                    onClick={() => setRemoving(true)}
                  >
                    Remove account
                  </Button>
                </div>
              </section>
            )}
            <ConnectDialog
              open={replacing}
              onOpenChange={setReplacing}
              cloud={c.cloud}
              replacing={c}
              onConnected={() => void connection.refetch()}
            />
            <ConnectDialog
              open={switching}
              onOpenChange={setSwitching}
              cloud={c.cloud}
              replacing={c}
              keyless
              onConnected={() => void connection.refetch()}
            />
            <CloudConfirm
              open={removing}
              onOpenChange={(open) => {
                if (!open) setRemoveError("")
                setRemoving(open)
              }}
              title={`Remove ${c.name}?`}
              confirmLabel="Remove account"
              pending={remove.isPending}
              onConfirm={() => void confirmRemove()}
              error={removeError && <CloudError message={removeError} />}
            >
              Loomplane deletes its credential and can&apos;t deploy to{" "}
              {c.account || cloudLabel(c.cloud)} until it&apos;s connected
              again. Nothing already running there is touched.
            </CloudConfirm>
          </div>
        )}
      </Page>
    </>
  )
}

/** RunningRow is one service or resource Loomplane runs on the account. */
function RunningRow({
  binding: b,
  project: p,
  env,
}: {
  binding: Binding
  project?: Project
  env: string
}) {
  const s = p ? projectStyle(p) : null
  const Icon = b.subject === "service" ? GlobeIcon : DatabaseIcon
  return (
    <CloudTableRow>
      <NameCell tile={<Icon />} name={b.name} meta={b.engine || b.subject} />
      <Cell className="flex items-center gap-2">
        {p && s && (
          <Link
            to="/projects/$id"
            params={{ id: p.id }}
            className="flex min-w-0 items-center gap-1.5 hover:underline"
          >
            <ProjectIcon icon={s.icon} color={s.color} size="xs" />
            <span className="truncate">{p.name}</span>
          </Link>
        )}
        <EnvBadge env={env} />
      </Cell>
      <Cell className="font-mono text-xs text-muted-foreground">
        {b.region || "Global"}
      </Cell>
      <Cell>
        {b.drift ? (
          <StatusPill tone="waiting">
            {b.drift === "missing"
              ? "Deleted outside Loomplane"
              : "Changed outside Loomplane"}
          </StatusPill>
        ) : (
          <StatusPill tone="live">Running</StatusPill>
        )}
      </Cell>
    </CloudTableRow>
  )
}

/** useSpend is the account's spend so far this month, as a fact's value. */
function useSpend(connectionId: string): ReactNode {
  const q = useQuery(useUsageSpend)
  const r = q.data
  const account = r?.accounts.find((a) => a.connectionId === connectionId)
  const source = r?.sources.find((s) => s.connectionId === connectionId)
  if (q.status === "error") return "Couldn't be loaded"
  if (!r) return <Skeleton className="ml-auto h-4 w-20" />
  if (account?.currency) return money(account.total, account.currency)
  if (account) return "No charges yet"
  return source?.reason || "Not read for this kind of account"
}
