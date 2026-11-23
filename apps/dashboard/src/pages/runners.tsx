import { CpuIcon, PencilIcon, PlusIcon, Trash2Icon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
import {
  usePlatformRunnerPoolsCreate,
  usePlatformRunnerPoolsJoinToken,
  usePlatformRunnerPoolsList,
  useRunnerPoolsCreate,
  useRunnerPoolsDelete,
  useRunnerPoolsJoinToken,
  useRunnerPoolsList,
  useRunnerPoolsUpdate,
} from "@/client/forge/loomplane/hooks"
import type {
  PoolView,
  RunnerView,
  RunnersJoinToken,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { CopyButton } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useInstance } from "@/lib/instance"
import { useOrganization } from "@/lib/organization"

/** Runners lists the organization's runner pools and their runners. */
export function Runners() {
  const { can } = useOrganization()
  const manage = can("nodes:manage")
  const { platformAdmin } = useInstance()
  const pools = useQuery(useRunnerPoolsList)
  const [creating, setCreating] = useState(false)
  const [joining, setJoining] = useState<{ pool: PoolView; global: boolean }>()
  const [editing, setEditing] = useState<PoolView>()
  const list = (pools.data?.pools ?? []).flatMap((p) => (p ? [p] : []))

  const add = manage ? (
    <Button onClick={() => setCreating(true)}>
      <PlusIcon />
      New pool
    </Button>
  ) : undefined

  return (
    <>
      <SiteHeader crumbs={[{ label: "Runners" }]} />
      <Page>
        <PageHeading
          title="Runners"
          description="Runners build your services, each build in its own Firecracker microVM, and deploy to servers only they can reach. Name a pool as a service's build.builder."
          actions={add}
        />
        {pools.status === "error" && (
          <QueryError
            error={pools.error}
            fallback="Runner pools couldn't be loaded."
            retry={pools.refetch}
          />
        )}
        {!pools.data && pools.status !== "error" && (
          <Skeleton className="h-48 w-full rounded-xl" />
        )}
        {pools.data && list.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <CpuIcon />
              </EmptyMedia>
              <EmptyTitle>No runner pools yet</EmptyTitle>
              <EmptyDescription>
                Make a pool, then start a runner on any Linux machine with KVM
                with one command.
              </EmptyDescription>
            </EmptyHeader>
            {add && <EmptyContent>{add}</EmptyContent>}
          </Empty>
        )}
        {list.map((pool) => (
          <PoolCard
            key={pool.id}
            pool={pool}
            manage={manage}
            onAddRunner={() => setJoining({ pool, global: false })}
            onEdit={() => setEditing(pool)}
          />
        ))}
        {platformAdmin && (
          <GlobalPools
            onAddRunner={(pool) => setJoining({ pool, global: true })}
          />
        )}
      </Page>
      {manage && <CreatePool open={creating} onOpenChange={setCreating} />}
      {joining && (
        <AddRunner
          pool={joining.pool}
          global={joining.global}
          onClose={() => setJoining(undefined)}
        />
      )}
      {manage && editing && (
        <EditPool pool={editing} onClose={() => setEditing(undefined)} />
      )}
    </>
  )
}

function PoolCard({
  pool,
  manage,
  onAddRunner,
  onEdit,
  global = false,
}: {
  pool: PoolView
  manage: boolean
  onAddRunner: () => void
  onEdit?: () => void
  global?: boolean
}) {
  const remove = useMutation(useRunnerPoolsDelete)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")
  const runners = (pool.runners ?? []).flatMap((r) => (r ? [r] : []))
  const ready = runners.some((r) => r.takesBuilds && r.status === "online")
  const onDelete = async () => {
    setError("")
    try {
      await remove.mutateAsync({ path: { id: pool.id } })
      invalidate(useRunnerPoolsList)
    } catch (err) {
      setError(messageOf(err, "The pool wasn't removed."))
    }
  }
  return (
    <Card className="gap-3 px-4 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <div className="flex items-center gap-2">
            <span className="text-[15px] font-semibold">{pool.name}</span>
            <Badge variant={ready ? "default" : "secondary"}>
              {ready ? "Ready" : "No runner online"}
            </Badge>
            <Badge variant="outline">
              {global
                ? "Every enabled workspace"
                : pool.scope === "project"
                  ? "One project"
                  : "Organization"}
            </Badge>
            {pool.mode === "on_demand" && (
              <Badge variant="outline">On demand</Badge>
            )}
            {pool.settings?.default && <Badge variant="outline">Default</Badge>}
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            {global ? `pool ${pool.id}` : `build.builder: ${pool.connectionId}`}
          </span>
          <span className="text-xs text-muted-foreground">
            {pool.runtimeIdentity
              ? `Pins ${pool.architecture} toolchain ${pool.runtimeIdentity.slice(0, 12)}…`
              : "Pins the toolchain of the first runner that passes its check"}
          </span>
        </div>
        {manage && (
          <div className="flex gap-2">
            <Button variant="outline" onClick={onAddRunner}>
              <PlusIcon />
              Add runner
            </Button>
            {onEdit && (
              <Button
                variant="ghost"
                aria-label={`Edit pool ${pool.name}`}
                onClick={onEdit}
              >
                <PencilIcon />
              </Button>
            )}
            {!global && (
              <Button
                variant="ghost"
                aria-label={`Remove pool ${pool.name}`}
                onClick={onDelete}
                disabled={remove.isPending}
              >
                <Trash2Icon />
              </Button>
            )}
          </div>
        )}
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {runners.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Runner</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Builds</TableHead>
              <TableHead className="hidden md:table-cell">Check</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runners.map((r) => (
              <RunnerRow key={r.nodeId} runner={r} />
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  )
}

function RunnerRow({ runner: r }: { runner: RunnerView }) {
  return (
    <TableRow>
      <TableCell className="text-[13.5px] font-medium">
        {r.name || r.nodeId}
      </TableCell>
      <TableCell>
        <Badge variant={r.status === "online" ? "default" : "secondary"}>
          {r.status}
        </Badge>
      </TableCell>
      <TableCell className="font-mono text-xs tabular-nums">
        {r.builds}
      </TableCell>
      <TableCell className="hidden text-[12.5px] md:table-cell">
        {r.takesBuilds ? (
          "Takes builds in microVMs"
        ) : (
          <span className="text-muted-foreground">
            {r.reason || "Not checked yet"}
          </span>
        )}
      </TableCell>
    </TableRow>
  )
}

function CreatePool({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const create = useMutation(useRunnerPoolsCreate)
  const invalidate = useInvalidate()
  const [name, setName] = useState("")
  const [project, setProject] = useState("")
  const [isDefault, setIsDefault] = useState(false)
  const [registry, setRegistry] = useState("")
  const [onDemand, setOnDemand] = useState(false)
  const [od, setOd] = useState({
    provider: "digitalocean",
    cloudConnectionId: "",
    region: "",
    sizes: "",
    image: "",
    maxRunners: "2",
    minWarm: "0",
    toolchainUrl: "",
    toolchainSha256: "",
  })
  const [error, setError] = useState("")
  const field = (key: keyof typeof od) => ({
    value: od[key],
    onChange: (e: React.ChangeEvent<HTMLInputElement>) =>
      setOd({ ...od, [key]: e.target.value }),
  })
  const submit = async () => {
    setError("")
    try {
      await create.mutateAsync({
        body: {
          name,
          scope: project ? "project" : "organization",
          ...(project && { projectId: project }),
          mode: onDemand ? "on_demand" : "self_hosted",
          settings: {
            ...(isDefault && { default: true }),
            ...(registry && { registry }),
            ...(onDemand && {
              provider: od.provider,
              cloudConnectionId: od.cloudConnectionId,
              region: od.region,
              sizes: od.sizes
                .split(",")
                .map((x) => x.trim())
                .filter(Boolean),
              ...(od.image && { image: od.image }),
              maxRunners: Number(od.maxRunners) || 0,
              minWarm: Number(od.minWarm) || 0,
              toolchainUrl: od.toolchainUrl,
              toolchainSha256: od.toolchainSha256,
            }),
          },
        },
      })
      invalidate(useRunnerPoolsList)
      setName("")
      setProject("")
      onOpenChange(false)
    } catch (err) {
      setError(messageOf(err, "The pool wasn't made."))
    }
  }
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
        <div className="grid gap-5 p-4">
          <SheetHeader className="p-0 pr-8">
            <SheetTitle className="text-lg font-semibold">
              New runner pool
            </SheetTitle>
            <SheetDescription>
              A pool groups runners. Services name it as their build.builder, or
              a Cloud names it as its build default.
            </SheetDescription>
          </SheetHeader>
          <div className="grid gap-1.5">
            <label htmlFor="pool-name" className="text-[13px] font-medium">
              Name
            </label>
            <Input
              id="pool-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="builds-amd64"
            />
          </div>
          <div className="grid gap-1.5">
            <label htmlFor="pool-project" className="text-[13px] font-medium">
              Project ID (optional)
            </label>
            <Input
              id="pool-project"
              value={project}
              onChange={(e) => setProject(e.target.value)}
              placeholder="Leave empty for every project"
            />
          </div>
          <div className="grid gap-1.5">
            <label htmlFor="pool-registry" className="text-[13px] font-medium">
              Registry connection (optional)
            </label>
            <Input
              id="pool-registry"
              value={registry}
              onChange={(e) => setRegistry(e.target.value)}
              placeholder="conn_... for builds whose manifest names none"
            />
          </div>
          <label className="flex items-start gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
            />
            <span>
              Default: build every service whose manifest names no builder
              {project ? " in this project" : " in the organization"}
            </span>
          </label>
          <label className="flex items-start gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={onDemand}
              onChange={(e) => setOnDemand(e.target.checked)}
            />
            <span>
              On demand: Loomplane starts runners in your DigitalOcean or AWS
              account when builds need them, and removes them after
            </span>
          </label>
          {onDemand && (
            <div className="grid gap-3 rounded-lg border p-3">
              {(
                [
                  ["provider", "Provider", "digitalocean or aws"],
                  ["cloudConnectionId", "Cloud connection", "conn_..."],
                  ["region", "Region", "nyc3, us-east-1"],
                  [
                    "sizes",
                    "Sizes, smallest first",
                    "s-2vcpu-4gb-amd, c8i.large",
                  ],
                  ["image", "Image", "AWS needs an ami-..."],
                  ["maxRunners", "Most runners at once", "2"],
                  ["minWarm", "Warm runners kept up", "0"],
                  [
                    "toolchainUrl",
                    "Toolchain bundle URL",
                    "https://.../toolchain.tar.gz",
                  ],
                  ["toolchainSha256", "Toolchain bundle sha256", "64 hex"],
                ] as const
              ).map(([key, label, placeholder]) => (
                <div key={key} className="grid gap-1">
                  <label
                    htmlFor={`pool-${key}`}
                    className="text-[12.5px] font-medium"
                  >
                    {label}
                  </label>
                  <Input
                    id={`pool-${key}`}
                    placeholder={placeholder}
                    {...field(key)}
                  />
                </div>
              ))}
            </div>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button onClick={submit} disabled={!name || create.isPending}>
            Create pool
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function AddRunner({
  pool,
  global = false,
  onClose,
}: {
  pool: PoolView
  global?: boolean
  onClose: () => void
}) {
  const own = useMutation(useRunnerPoolsJoinToken)
  const platform = useMutation(usePlatformRunnerPoolsJoinToken)
  const mint = global ? platform : own
  const [token, setToken] = useState<RunnersJoinToken>()
  const [error, setError] = useState("")
  const make = async () => {
    setError("")
    try {
      setToken(
        (await mint.mutateAsync({
          path: { id: pool.id },
          body: {},
        })) as RunnersJoinToken
      )
    } catch (err) {
      setError(messageOf(err, "The token wasn't made."))
    }
  }
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
        <div className="grid gap-5 p-4">
          <SheetHeader className="p-0 pr-8">
            <SheetTitle className="text-lg font-semibold">
              Add a runner to {pool.name}
            </SheetTitle>
            <SheetDescription>
              Use a Linux machine with KVM: bare metal, or a VM with nested
              virtualization. Install the runner's Firecracker release, then run
              the command as root. The runner comes back after a crash or a
              reboot until you run loomplane runner stop on it.
            </SheetDescription>
          </SheetHeader>
          {!token && (
            <Button onClick={make} disabled={mint.isPending}>
              Make a join token
            </Button>
          )}
          {token && (
            <div className="grid gap-2">
              <span className="text-[13px] font-medium">
                Run this on the machine. The token works once, for an hour.
              </span>
              <div className="flex items-start gap-2 rounded-lg border bg-muted/40 p-3">
                <code className="font-mono text-xs break-all">
                  {token.command}
                </code>
                <CopyButton value={token.command} />
              </div>
              <span className="text-xs text-muted-foreground">
                Add --relay-allow with the networks it may deploy to, such as
                --relay-allow 10.0.0.0/16, or --once to run it in a terminal
                only.
              </span>
            </div>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

/** GlobalPools is the platform admin's: the pools every workspace enabled
 * for Loomplane Cloud may build on. */
function GlobalPools({
  onAddRunner,
}: {
  onAddRunner: (pool: PoolView) => void
}) {
  const pools = useQuery(usePlatformRunnerPoolsList)
  const create = useMutation(usePlatformRunnerPoolsCreate)
  const invalidate = useInvalidate()
  const [name, setName] = useState("")
  const [isDefault, setIsDefault] = useState(true)
  const [error, setError] = useState("")
  const list = (pools.data?.pools ?? []).flatMap((p) => (p ? [p] : []))
  const submit = async () => {
    setError("")
    try {
      await create.mutateAsync({
        body: {
          name,
          scope: "platform",
          settings: isDefault ? { default: true } : {},
        },
      })
      invalidate(usePlatformRunnerPoolsList)
      setName("")
    } catch (err) {
      setError(messageOf(err, "The pool wasn't made."))
    }
  }
  return (
    <section className="grid gap-3">
      <div className="grid gap-1">
        <h2 className="text-[15px] font-semibold">Global runners</h2>
        <p className="text-[13px] text-muted-foreground">
          Your runners for every workspace you enabled for Loomplane Cloud.
          Their builds run in microVMs under each workspace's limits and are
          billed as build minutes. Run them in platform mode: SMT off, mail
          denied, per-workspace limits.
        </p>
      </div>
      {list.map((pool) => (
        <PoolCard
          key={pool.id}
          pool={pool}
          manage
          global
          onAddRunner={() => onAddRunner(pool)}
        />
      ))}
      <Card className="gap-3 px-4 py-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1.5">
            <label htmlFor="global-name" className="text-[13px] font-medium">
              New global pool
            </label>
            <Input
              id="global-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="global-amd64"
            />
          </div>
          <label className="flex items-center gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
            />
            Default for workspaces with none of their own
          </label>
          <Button onClick={submit} disabled={!name || create.isPending}>
            Create global pool
          </Button>
        </div>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </Card>
    </section>
  )
}

/** EditPool changes a pool's settings: whether it is the default, the
 * registry its defaulted builds push to, and an on-demand pool's runners. */
function EditPool({ pool, onClose }: { pool: PoolView; onClose: () => void }) {
  const update = useMutation(useRunnerPoolsUpdate)
  const invalidate = useInvalidate()
  const settings = pool.settings ?? {}
  const [isDefault, setIsDefault] = useState(!!settings.default)
  const [registry, setRegistry] = useState(settings.registry ?? "")
  const [maxRunners, setMaxRunners] = useState(
    String(settings.maxRunners ?? "")
  )
  const [minWarm, setMinWarm] = useState(String(settings.minWarm ?? ""))
  const [error, setError] = useState("")
  const onDemand = pool.mode === "on_demand"
  const save = async () => {
    setError("")
    try {
      await update.mutateAsync({
        path: { id: pool.id },
        body: {
          ...settings,
          default: isDefault,
          registry,
          ...(onDemand && {
            maxRunners: Number(maxRunners) || 0,
            minWarm: Number(minWarm) || 0,
          }),
        },
      })
      invalidate(useRunnerPoolsList)
      onClose()
    } catch (err) {
      setError(messageOf(err, "The pool wasn't changed."))
    }
  }
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
        <div className="grid gap-5 p-4">
          <SheetHeader className="p-0 pr-8">
            <SheetTitle className="text-lg font-semibold">
              Edit {pool.name}
            </SheetTitle>
            <SheetDescription>
              A new default takes effect on the next plan. Another pool that is
              the default has to give it up first.
            </SheetDescription>
          </SheetHeader>
          <label className="flex items-start gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
            />
            <span>Default for services whose manifest names no builder</span>
          </label>
          <div className="grid gap-1.5">
            <label htmlFor="edit-registry" className="text-[13px] font-medium">
              Registry connection
            </label>
            <Input
              id="edit-registry"
              value={registry}
              onChange={(e) => setRegistry(e.target.value)}
              placeholder="conn_..."
            />
          </div>
          {onDemand && (
            <>
              <div className="grid gap-1.5">
                <label htmlFor="edit-max" className="text-[13px] font-medium">
                  Most runners at once
                </label>
                <Input
                  id="edit-max"
                  value={maxRunners}
                  onChange={(e) => setMaxRunners(e.target.value)}
                />
              </div>
              <div className="grid gap-1.5">
                <label htmlFor="edit-warm" className="text-[13px] font-medium">
                  Warm runners kept up
                </label>
                <Input
                  id="edit-warm"
                  value={minWarm}
                  onChange={(e) => setMinWarm(e.target.value)}
                />
              </div>
            </>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button onClick={save} disabled={update.isPending}>
            Save
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  )
}
