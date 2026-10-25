// One connected account: whether Loomplane can still use it, what it can
// run, what Loomplane runs on it now, and replacing or removing it.
import { ChevronLeftIcon, DatabaseIcon, GlobeIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsCheck,
  useConnectionsGet,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import {
  ConnectDialog,
  needsSignInAgain,
  RemoveDialog,
  statusLabels,
  useSignInAgain,
} from "@/components/cloud-connections"
import { Page } from "@/components/page"
import { EnvBadge, StatusPill, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { AccountSpend } from "@/components/usage"
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { cloudPurposes } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { useNow, usePeople, useRunning } from "@/lib/use-runs"

const tone: Record<string, Tone> = {
  ready: "live",
  incomplete: "waiting",
  failed: "failed",
  sign_in_needed: "waiting",
}

export function CloudAccount({ id }: { id: string }) {
  const { can } = useOrganization()
  const now = useNow()
  const person = usePeople()
  const connection = useQuery(useConnectionsGet, { path: { id } })
  const check = useMutation(useConnectionsCheck)
  const { signInAgain, pending: signingIn } = useSignInAgain()
  const running = useRunning()
  const projects = useQuery(useProjectsList)
  const [replacing, setReplacing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const c = connection.data
  const manage = can("cloud_account:manage")
  const here = running.bindings.filter((b) => b.connectionId === id)
  const services = here.filter((b) => b.subject === "service").length
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

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Clouds", to: "/clouds" },
          { label: c?.name ?? "Account" },
        ]}
      />
      <Page>
        <Link
          to="/clouds"
          className="-mb-2 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          Clouds
        </Link>
        {connection.status === "error" && !missing ? (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(connection.error, "The account couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        ) : missing ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>No connected account here</EmptyTitle>
              <EmptyDescription>
                It may have been removed, or it belongs to another organization.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button nativeButton={false} render={<Link to="/clouds" />}>
                Clouds
              </Button>
            </EmptyContent>
          </Empty>
        ) : !c ? (
          <div className="grid gap-4" aria-busy="true">
            <Skeleton className="h-12 w-80" />
            <Skeleton className="h-28 rounded-xl" />
          </div>
        ) : (
          <>
            <header className="flex flex-wrap items-start justify-between gap-4">
              <div className="flex items-center gap-3.5">
                <CloudGlyph cloud={c.cloud} size="xl" />
                <div className="grid gap-1">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <h1 className="text-2xl leading-tight font-semibold tracking-[-0.025em]">
                      {c.name}
                    </h1>
                    <StatusPill tone={tone[c.status] ?? "quiet"}>
                      {statusLabels[c.status] ?? c.status}
                    </StatusPill>
                  </div>
                  <span className="text-[13px] text-muted-foreground">
                    {cloudLabel(c.cloud)}
                    {c.account && (
                      <>
                        {" · "}
                        <code className="font-mono">{c.account}</code>
                      </>
                    )}
                    {" · connected "}
                    {formatDate(c.createdAt)} by {person(c.createdBy).name}
                  </span>
                </div>
              </div>
              {manage && (
                <div className="flex flex-wrap gap-2">
                  {needsSignInAgain(c) && (
                    <Button
                      disabled={signingIn}
                      onClick={() => void signInAgain(c)}
                    >
                      {signingIn && <Spinner />}
                      Sign in again
                    </Button>
                  )}
                  <Button
                    variant="outline"
                    disabled={check.isPending}
                    onClick={() => void recheck()}
                  >
                    {check.isPending && <Spinner />}
                    Check again
                  </Button>
                  <Button variant="outline" onClick={() => setReplacing(true)}>
                    Replace credential
                  </Button>
                </div>
              )}
            </header>

            {c.status !== "ready" && (
              <Alert className="rounded-xl">
                <AlertDescription>
                  {c.message ||
                    "Loomplane can't use this account for every deployment yet."}
                  {c.missing.length > 0 && ` Missing: ${c.missing.join(", ")}.`}
                </AlertDescription>
              </Alert>
            )}

            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <Stat
                label="Last checked"
                value={
                  c.checkedAt ? relative(Date.parse(c.checkedAt), now) : "Never"
                }
                note={
                  c.status === "ready"
                    ? "Can do everything a deployment needs"
                    : statusLabels[c.status]
                }
              />
              <Stat
                label="Running on it"
                value={String(here.length)}
                note={`${services} ${services === 1 ? "service" : "services"}, ${here.length - services} ${here.length - services === 1 ? "resource" : "resources"}`}
                mono
              />
              <Stat
                label="Can run"
                value={cloudLabel(c.cloud)}
                note={cloudPurposes[c.cloud] ?? "Services Loomplane deploys"}
              />
              <AccountSpend connectionId={id} />
            </div>

            <section className="overflow-hidden rounded-xl bg-card ring-1 ring-border">
              <h2 className="border-b px-4 py-3 text-sm font-semibold">
                What runs here
              </h2>
              {!running.loaded && here.length === 0 ? (
                <div className="p-4">
                  <Skeleton className="h-5 w-72" />
                </div>
              ) : here.length === 0 ? (
                <p className="px-4 py-4 text-[13px] text-muted-foreground">
                  Nothing Loomplane deployed runs on this account yet.
                </p>
              ) : (
                <ul className="divide-y">
                  {here.map((b) => {
                    const p = projects.data?.projects.find(
                      (x) => x.id === b.projectId
                    )
                    const s = p ? projectStyle(p) : null
                    const Icon =
                      b.subject === "service" ? GlobeIcon : DatabaseIcon
                    return (
                      <li
                        key={`${b.environmentId}:${b.subject}:${b.name}`}
                        className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_100px] items-center gap-3 px-4 py-2.5 text-[13px]"
                      >
                        <span className="flex min-w-0 items-center gap-2.5">
                          <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted">
                            <Icon className="size-3.5" />
                          </span>
                          <span className="grid min-w-0">
                            <span className="truncate font-medium">
                              {b.name}
                            </span>
                            <span className="truncate text-xs text-muted-foreground">
                              {b.engine || b.subject}
                            </span>
                          </span>
                        </span>
                        <span className="flex min-w-0 items-center gap-2">
                          {p && s && (
                            <Link
                              to="/projects/$id"
                              params={{ id: p.id }}
                              className="flex min-w-0 items-center gap-1.5 hover:underline"
                            >
                              <ProjectIcon
                                icon={s.icon}
                                color={s.color}
                                size="xs"
                              />
                              <span className="truncate">{p.name}</span>
                            </Link>
                          )}
                          <EnvBadge
                            env={running.envNames.get(b.environmentId) ?? ""}
                          />
                        </span>
                        <span className="font-mono text-xs text-muted-foreground">
                          {b.region || "Global"}
                        </span>
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>

            {manage && (
              <section className="flex flex-wrap items-center gap-3 rounded-xl px-4 py-3.5 ring-1 ring-[color-mix(in_oklch,var(--destructive)_25%,transparent)]">
                <div className="grid flex-[1_1_360px] gap-1">
                  <h2 className="text-sm font-semibold">Remove {c.name}</h2>
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
                  Remove
                </Button>
              </section>
            )}
            <ConnectDialog
              open={replacing}
              onOpenChange={setReplacing}
              cloud={c.cloud}
              replacing={c}
              onConnected={() => void connection.refetch()}
            />
            <RemoveDialog
              connection={c}
              open={removing}
              onOpenChange={setRemoving}
            />
          </>
        )}
      </Page>
    </>
  )
}

function Stat({
  label,
  value,
  note,
  mono = false,
}: {
  label: string
  value: string
  note?: string
  mono?: boolean
}) {
  return (
    <section className="grid gap-1 rounded-xl bg-card p-4 ring-1 ring-border">
      <span className="text-[12.5px] text-muted-foreground">{label}</span>
      <span
        className={
          mono
            ? "font-mono text-[22px] font-medium tabular-nums"
            : "text-lg font-semibold"
        }
      >
        {value}
      </span>
      {note && (
        <span className="text-[12.5px] leading-snug text-muted-foreground">
          {note}
        </span>
      )}
    </section>
  )
}
