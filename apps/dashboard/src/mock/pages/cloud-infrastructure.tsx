import {
  ActivityIcon as ActivityPulseIcon,
  RoutesIcon,
} from "@/components/icons"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useState } from "react"
import {
  validExternalTTL,
  canUpdateExternalTTL,
} from "@/lib/cloud-infrastructure"
import { useNow } from "@/lib/use-runs"
import type { CloudTab } from "@/components/cloud-targets/infrastructure-ui"
import {
  CloudEmpty,
  CloudError,
  Field,
  Rows,
  Row,
} from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"

type Host = {
  id: string
  name: string
  ownership: string
  size: string
  status: string
  draining: boolean
}
type Domain = {
  hostname: string
  mode: string
  status: string
  dns: boolean
  tls: boolean
  backend: boolean
  ttl: number
  revision: number
}
type MovePin = {
  sourceId: string
  destinationId: string
  workloadId: string
  authority: string
  expiresAt: number
}
type Review = {
  kind: string
  server?: Host
  name: string
  size: string
  token: number
}
const initialHosts: Host[] = [
  {
    id: "us-1",
    name: "shared-us-1",
    ownership: "managed",
    size: "2 CPU / 4 GiB",
    status: "ready",
    draining: false,
  },
  {
    id: "us-2",
    name: "shared-us-2",
    ownership: "adopted",
    size: "4 CPU / 8 GiB",
    status: "ready",
    draining: false,
  },
]
type Workload = {
  id: string
  project: string
  service: string
  serverId: string
}
const initialWorkloads: Workload[] = [
  { id: "web-api", project: "Web app", service: "api", serverId: "us-1" },
  { id: "jobs-worker", project: "Jobs", service: "worker", serverId: "us-1" },
]
export function MockCloudInfrastructure({
  tab,
  failure,
  onPoolsChange,
}: {
  tab: CloudTab
  failure: boolean
  onPoolsChange?: (names: string[]) => void
}) {
  const [hosts, setHosts] = useState(initialHosts)
  const [workloads, setWorkloads] = useState(initialWorkloads)
  const workloadsOn = (serverId: string) =>
    workloads.filter((workload) => workload.serverId === serverId)
  const workloadCount = (serverId: string) => workloadsOn(serverId).length
  const [pool, setPool] = useState(false)
  const [poolDialog, setPoolDialog] = useState(false)
  const [poolMembers, setPoolMembers] = useState(
    initialHosts.map((h) => ({
      serverId: h.id,
      cpuMillis: h.size.startsWith("4") ? 4000 : 2000,
      memoryMib: h.size.startsWith("4") ? 8192 : 4096,
      draining: h.draining,
    }))
  )
  const [moveReviewed, setMoveReviewed] = useState<MovePin | null>(null)
  const [moveDestination, setMoveDestination] = useState("")
  const [movePlanned, setMovePlanned] = useState(false)
  const [moveAccepted, setMoveAccepted] = useState(false)
  const [retained, setRetained] = useState(true)
  const [grant, setGrant] = useState(["Web app"])
  const [domains, setDomains] = useState<Domain[]>([])
  const [events, setEvents] = useState<string[]>([])
  const [editing, setEditing] = useState<{
    kind: string
    server?: Host
  } | null>(null)
  const [name, setName] = useState("")
  const [size, setSize] = useState("2 CPU / 4 GiB")
  const [confirmed, setConfirmed] = useState(false)
  const [review, setReview] = useState<Review | null>(null)
  const [error, setError] = useState("")
  const [generation, setGeneration] = useState(1)
  const [domainDialog, setDomainDialog] = useState(false)
  const [hostname, setHostname] = useState("api.example.test")
  const [mode, setMode] = useState("external")
  const [ttl, setTTL] = useState("300")
  const [zone, setZone] = useState(false)
  const [zoneVerified, setZoneVerified] = useState(false)
  const [routeReview, setRouteReview] = useState(false)
  const now = useNow()
  const moveSource = hosts.find(
    (host) =>
      host.draining &&
      workloads.some(
        (workload) => workload.id === "web-api" && workload.serverId === host.id
      )
  )
  const moveEligible = hosts.filter(
    (host) =>
      host.status === "ready" &&
      !host.draining &&
      host.id !== moveSource?.id &&
      poolMembers.some(
        (member) => member.serverId === host.id && !member.draining
      )
  )
  const moveTarget = moveDestination
    ? moveEligible.find((host) => host.id === moveDestination)
    : moveEligible[0]
  const moveAuthority = JSON.stringify({
    generation,
    members: poolMembers,
    workloads,
    hosts: hosts.map((host) => [
      host.id,
      host.status,
      host.draining,
      host.ownership,
      host.size,
    ]),
  })
  const currentMove = (at: number) =>
    !!moveReviewed &&
    moveReviewed.authority === moveAuthority &&
    moveReviewed.sourceId === moveSource?.id &&
    moveReviewed.destinationId === moveTarget?.id &&
    workloads.some(
      (workload) =>
        workload.id === moveReviewed.workloadId &&
        workload.id === "web-api" &&
        workload.serverId === moveReviewed.sourceId
    ) &&
    at < moveReviewed.expiresAt
  const moveValid = currentMove(now)

  const record = (event: string) => setEvents((events) => [event, ...events])
  const open = (kind: string, server?: Host) => {
    setEditing({ kind, server })
    setName(server?.name ?? "")
    setSize(server?.size ?? "2 CPU / 4 GiB")
    setReview(null)
    setConfirmed(false)
    setError("")
  }
  function apply() {
    if (!review || review.token !== generation) {
      setReview(null)
      setError("The connection changed. Review the server again.")
      return
    }
    const { kind, server } = review
    if (kind === "recovery") {
      setRetained(false)
      record(
        "Reviewed reservation recovery released inactive compute; retained storage remains explicitly recorded."
      )
      setEditing(null)
      return
    }
    if (kind === "create" || kind === "adopt")
      setHosts([
        ...hosts,
        {
          id: crypto.randomUUID(),
          name,
          ownership: kind === "create" ? "managed" : "adopted",
          size,
          status: "waiting",
          draining: false,
        },
      ])
    else if (kind === "delete" || kind === "disconnect")
      setHosts(hosts.filter((h) => h.id !== server?.id))
    else
      setHosts(
        hosts.map((h) =>
          h.id === server?.id
            ? { ...h, size, draining: kind === "drain" ? true : h.draining }
            : h
        )
      )
    record(
      `${kind} ${name}: accepted. Review enrollment, applied health and retained cleanup.`
    )
    setEditing(null)
  }
  function verify(domain: Domain, retry = false) {
    const failed = failure && !retry
    setDomains(
      domains.map((d) =>
        d.hostname === domain.hostname
          ? {
              ...d,
              dns: true,
              tls: !failed,
              backend: !failed,
              status: failed ? "waiting" : "ready",
            }
          : d
      )
    )
    record(
      `${domain.hostname}: ${failed ? "DNS verified, TLS pending. Retry after resolving certificate issuance." : "DNS, TLS and backend verified."}`
    )
  }
  return (
    <>
      {tab === "Servers" && (
        <section className="grid gap-3" aria-label="Mock servers">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-[15px] font-semibold">
              {hosts.length} servers
            </h2>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => open("adopt")}>
                Connect existing server
              </Button>
              <Button onClick={() => open("create")}>Create server</Button>
            </div>
          </div>
          <Rows>
            {hosts.map((host) => (
              <Row key={host.id}>
                <div className="grid gap-1">
                  <p className="font-medium">
                    {host.name} · {host.status}
                    {host.draining ? " · draining" : ""}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {host.ownership} · us-east · {host.size} ·{" "}
                    {workloadCount(host.id)} workloads
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {workloadsOn(host.id)
                      .map(
                        (workload) =>
                          `${workload.project} / ${workload.service}`
                      )
                      .join(" · ") || "No active services"}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    Reserved {workloadCount(host.id) * 500} mCPU /{" "}
                    {workloadCount(host.id) * 256} MiB · available{" "}
                    {(host.size.startsWith("4") ? 4000 : 2000) -
                      workloadCount(host.id) * 500}{" "}
                    mCPU /{" "}
                    {(host.size.startsWith("4") ? 8192 : 4096) -
                      workloadCount(host.id) * 256}{" "}
                    MiB
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  {host.status === "waiting" && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        setHosts(
                          hosts.map((h) =>
                            h.id === host.id ? { ...h, status: "ready" } : h
                          )
                        )
                        record(
                          `${host.name}: enrollment and runtime health verified.`
                        )
                      }}
                    >
                      Verify enrollment
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => open("drain", host)}
                  >
                    Inspect / drain
                  </Button>
                  {host.ownership === "managed" && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => open("resize", host)}
                    >
                      Resize
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={
                      workloadCount(host.id) > 0 ||
                      (host.id === "us-1" && retained)
                    }
                    onClick={() =>
                      open(
                        host.ownership === "managed" ? "delete" : "disconnect",
                        host
                      )
                    }
                  >
                    {host.ownership === "managed"
                      ? "Review deletion"
                      : "Review disconnect"}
                  </Button>
                </div>
              </Row>
            ))}
          </Rows>
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-muted p-3 text-sm">
            <p>
              {pool
                ? `shared-services pool · ${poolMembers.length} members · reviewed CPU and memory budgets`
                : "No server pool yet"}
            </p>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPoolDialog(true)}
            >
              {pool ? "Edit pool budgets" : "Add pool"}
            </Button>
            {pool && (
              <>
                <Field label="Move destination server">
                  <Select
                    value={moveTarget?.id ?? ""}
                    onValueChange={(event) => setMoveDestination(event ?? "")}
                  >
                    <SelectTrigger
                      aria-label="Move destination server"
                      className={"w-full min-w-0"}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">
                        Choose eligible destination
                      </SelectItem>
                      {moveEligible.map((host) => (
                        <SelectItem key={host.id} value={host.id}>
                          {host.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                {!moveEligible.length && (
                  <p className="basis-full text-xs">
                    No eligible destination. Add a ready, non-draining server to
                    this pool.
                  </p>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={
                    !moveSource || !moveTarget || (moveAccepted && moveValid)
                  }
                  onClick={() => {
                    if (!currentMove(Date.now())) {
                      if (!moveSource || !moveTarget) return
                      setMoveReviewed({
                        sourceId: moveSource.id,
                        destinationId: moveTarget.id,
                        workloadId: "web-api",
                        authority: moveAuthority,
                        expiresAt: Date.now() + 15 * 60 * 1000,
                      })
                      setMoveAccepted(false)
                      setMovePlanned(false)
                      return
                    }
                    setMoveReviewed((pin) =>
                      pin
                        ? { ...pin, expiresAt: Date.now() + 15 * 60 * 1000 }
                        : null
                    )
                    setMoveAccepted(true)
                    record(
                      `Reviewed api move staged for ${moveTarget?.name}. Source still serves until a deployment plan is reviewed.`
                    )
                  }}
                >
                  {moveValid
                    ? "Accept reviewed service move"
                    : "Preview service move"}
                </Button>
                {moveReviewed && !moveValid && (
                  <Alert variant="destructive" className="basis-full text-xs">
                    <AlertDescription>
                      Pool membership, server authority or expiry changed.
                      Review the move again.
                    </AlertDescription>
                  </Alert>
                )}
                {moveValid && (
                  <p className="basis-full text-xs">
                    Move Web app / api from {moveSource?.name} to{" "}
                    {moveTarget?.name}. One service moves; other project
                    services keep their current bindings. Persisted release and
                    destination authority are pinned; storage migration stays
                    explicit.
                  </p>
                )}
                {moveAccepted && moveValid && (
                  <>
                    <p role="status" className="basis-full text-xs">
                      Move intent staged. It expires 15 minutes after
                      acceptance.{" "}
                      {movePlanned
                        ? "Deployment plan awaits review; the source still serves."
                        : "No deployment has started; the source still serves."}
                    </p>
                    {!movePlanned ? (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          if (!currentMove(Date.now())) return
                          setMovePlanned(true)
                          record(
                            "Deployment plan created for review. The source still serves."
                          )
                        }}
                      >
                        Create deployment plan
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          if (!currentMove(Date.now()) || !moveReviewed) return
                          const pin = moveReviewed
                          setWorkloads((current) =>
                            current.map((workload) =>
                              workload.id === pin.workloadId &&
                              workload.serverId === pin.sourceId
                                ? { ...workload, serverId: pin.destinationId }
                                : workload
                            )
                          )
                          setMoveAccepted(false)
                          setMovePlanned(false)
                          setMoveReviewed(null)
                          record(
                            "Reviewed deployment completed: destination release and routes verified. Retained cleanup still needs recovery."
                          )
                        }}
                      >
                        Simulate reviewed deployment completion
                      </Button>
                    )}
                  </>
                )}
              </>
            )}
            {retained && (
              <>
                <p className="basis-full text-xs">
                  shared-us-1 has retained cleanup. Preserved storage does not
                  confirm a named volume exists.
                </p>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={hosts.some(
                    (h) => h.id === "us-1" && workloadCount(h.id) > 0
                  )}
                  onClick={() =>
                    open(
                      "recovery",
                      hosts.find((h) => h.id === "us-1")
                    )
                  }
                >
                  Preview reservation recovery
                </Button>
              </>
            )}
          </div>
        </section>
      )}
      {tab === "Domains" && (
        <section className="grid gap-3" aria-label="Mock domains">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-[15px] font-semibold">
              DNS zones and HTTPS routes
            </h2>
            <Button
              onClick={() => {
                setDomainDialog(true)
                setRouteReview(false)
                setError("")
              }}
            >
              Add route
            </Button>
          </div>
          <div className="grid gap-2 rounded-lg bg-muted p-3 text-sm">
            <p>
              {zone
                ? `example.test · ${mode} DNS · ${zoneVerified ? "ownership verified" : "ownership pending"}`
                : "No DNS zone yet"}
            </p>
            {zone && mode === "external" && !zoneVerified && (
              <p className="text-xs break-all">
                Publish TXT _loomplane.example.test = fixture-proof, then verify
                ownership.
              </p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setZone(true)
                  record("DNS zone saved independently of compute account.")
                }}
              >
                Save DNS zone
              </Button>
              {zone && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setZoneVerified(true)
                    record(
                      "Zone ownership verified. Route DNS, TLS and backend remain separate."
                    )
                  }}
                >
                  Verify zone
                </Button>
              )}
            </div>
          </div>
          {!domains.length ? (
            <CloudEmpty kind="list" icon={RoutesIcon} title="No routes yet">
              Grant zone access, then review a deployed service's hostname and
              DNS authority.
            </CloudEmpty>
          ) : (
            <Rows>
              {domains.map((domain) => (
                <Row key={domain.hostname}>
                  <div className="grid gap-1">
                    <p className="font-medium break-all">
                      {domain.hostname} · {domain.status}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Web app / production / api · destination shared-us-1
                    </p>
                    <p className="text-xs">
                      DNS {domain.dns ? "verified" : "pending"} · TLS{" "}
                      {domain.tls ? "verified" : "pending"} · Backend{" "}
                      {domain.backend ? "verified" : "pending"}
                    </p>
                    {!domain.dns && (
                      <p className="text-xs break-all">
                        Publish A {domain.hostname} = 192.0.2.20 · TTL 300s.
                        Maximum previously served TTL: {domain.ttl}s.
                      </p>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => verify(domain)}
                    >
                      Verify route
                    </Button>
                    {domain.status === "waiting" && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => verify(domain, true)}
                      >
                        Retry route operation
                      </Button>
                    )}
                    {canUpdateExternalTTL(domain.status, domain.mode) && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setHostname(domain.hostname)
                          setTTL(String(domain.ttl))
                          setEditing({ kind: "ttl" })
                          setError("")
                        }}
                      >
                        Update external TTL
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setHostname(domain.hostname)
                        setEditing({ kind: "remove-route" })
                        setConfirmed(false)
                      }}
                    >
                      Remove route
                    </Button>
                  </div>
                </Row>
              ))}
            </Rows>
          )}
        </section>
      )}
      {tab === "Projects" && (
        <section className="grid gap-3" aria-label="Mock project access">
          <h2 className="text-[15px] font-semibold">Project access</h2>
          <p className="text-xs text-muted-foreground">
            Access to capacity does not grant another project's releases,
            variables, logs or routes.
          </p>
          <Rows>
            {["Web app", "Jobs"].map((project) => (
              <Row key={project}>
                <div>
                  <p>{project}</p>
                  <p className="text-xs text-muted-foreground">
                    {grant.includes(project)
                      ? "Granted targets: frontend, backend"
                      : "No Cloud grant"}
                  </p>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setGrant(
                      grant.includes(project)
                        ? grant.filter((p) => p !== project)
                        : [...grant, project]
                    )
                    record(
                      `${project}: project target access ${grant.includes(project) ? "revoked" : "granted"}.`
                    )
                  }}
                >
                  {grant.includes(project) ? "Revoke access" : "Give access"}
                </Button>
              </Row>
            ))}
          </Rows>
        </section>
      )}
      {tab === "Activity" && (
        <section className="grid gap-3" aria-label="Mock activity">
          <h2 className="text-[15px] font-semibold">Activity</h2>
          <p className="text-xs text-muted-foreground">
            Desired placement can advance while the prior applied release still
            serves. Retained cleanup stays visible until verified.
          </p>
          <div className="rounded-lg bg-muted p-3 text-xs">
            <p>Server recovery: restore exact retained runtime.</p>
            <p>Managed recovery: recreate from saved inputs.</p>
            <p>
              Mixed recovery: restore retained server runtimes and recreate
              managed services. Pending mixed effects without quiescence need
              reconciliation before a new review.
            </p>
          </div>
          {events.length ? (
            <Rows>
              {events.map((event, i) => (
                <Row className="p-3 text-[13px]" key={i}>
                  {event}
                </Row>
              ))}
            </Rows>
          ) : (
            <CloudEmpty
              kind="list"
              icon={ActivityPulseIcon}
              title="No infrastructure activity"
            >
              Review and apply a server, route or project change to see its
              checkpoints.
            </CloudEmpty>
          )}
        </section>
      )}
      {editing && (
        <Dialog open onOpenChange={(o) => !o && setEditing(null)}>
          <DialogContent className="max-h-[90dvh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>
                {editing.kind === "ttl"
                  ? "Update external TTL"
                  : editing.kind === "remove-route"
                    ? "Remove route"
                    : `Review ${editing.kind} server`}
              </DialogTitle>
              <DialogDescription>
                {editing.kind === "disconnect"
                  ? "Disconnect preserves your existing server."
                  : editing.kind === "remove-route"
                    ? "Remove only this route's owned DNS and ingress. Other services keep their routes."
                    : "Review scope, current authority and retained cleanup before applying."}
              </DialogDescription>
            </DialogHeader>
            {editing.kind === "ttl" ? (
              <>
                <Field label="Maximum previously served TTL (seconds)">
                  <Input
                    aria-label="Maximum previously served TTL (seconds)"
                    type="number"
                    min={60}
                    max={86400}
                    value={ttl}
                    onChange={(e) => setTTL(e.target.value)}
                  />
                </Field>
                <p className="text-xs">
                  Use your DNS host's previous TTL. The new record TTL and a
                  resolver response cannot establish this bound.
                </p>
                <Button
                  disabled={!validExternalTTL(Number(ttl))}
                  onClick={() => {
                    const route = domains.find(
                      (domain) => domain.hostname === hostname
                    )
                    if (
                      !route ||
                      !canUpdateExternalTTL(route.status, route.mode) ||
                      !validExternalTTL(Number(ttl))
                    )
                      return
                    setDomains(
                      domains.map((d) =>
                        d.hostname === hostname
                          ? { ...d, ttl: Number(ttl), revision: d.revision + 1 }
                          : d
                      )
                    )
                    record(
                      `${hostname}: external TTL bound updated; old reviews invalidated.`
                    )
                    setEditing(null)
                  }}
                >
                  Save TTL bound
                </Button>
              </>
            ) : editing.kind === "remove-route" ? (
              <Button
                variant="destructive"
                onClick={() => {
                  if (!confirmed) {
                    setConfirmed(true)
                    return
                  }
                  setDomains(domains.filter((d) => d.hostname !== hostname))
                  record(
                    `${hostname}: owned route removed. Unrelated DNS retained.`
                  )
                  setEditing(null)
                }}
              >
                {confirmed
                  ? "Confirm remove route"
                  : "Review owned route removal"}
              </Button>
            ) : (
              <>
                <Field label="Server name">
                  <Input
                    aria-label="Server name"
                    value={name}
                    disabled={!!editing.server}
                    onChange={(e) => {
                      setName(e.target.value)
                      setReview(null)
                    }}
                  />
                </Field>
                {["resize", "create"].includes(editing.kind) && (
                  <Field label="Size">
                    <Select
                      value={size}
                      onValueChange={(e) => {
                        setSize(e ?? "")
                        setReview(null)
                      }}
                    >
                      <SelectTrigger
                        aria-label="Server size"
                        className={"w-full min-w-0"}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="2 CPU / 4 GiB">
                          2 CPU / 4 GiB
                        </SelectItem>
                        <SelectItem value="4 CPU / 8 GiB">
                          4 CPU / 8 GiB
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                )}
                {editing.kind === "adopt" && (
                  <FormField
                    orientation="horizontal"
                    className="flex items-center gap-2 text-xs"
                  >
                    <FieldLabel className="contents">
                      <Checkbox
                        checked={confirmed}
                        onCheckedChange={(e) => setConfirmed(e)}
                      />
                      Confirm pinned SSH access and Docker installation
                    </FieldLabel>
                  </FormField>
                )}
                {editing.server && (
                  <p className="text-xs">
                    {workloadsOn(editing.server.id)
                      .map(
                        (workload) =>
                          `${workload.project} / ${workload.service}`
                      )
                      .join(", ") || "No active services"}
                    : {workloadCount(editing.server.id)} workloads. Drain
                    prevents new placement; it does not migrate data. Retained
                    cleanup must finish before removal.
                  </p>
                )}
                {review && (
                  <p className="rounded-lg bg-muted p-3 text-sm">
                    {review.kind}: {review.name}. Connection generation{" "}
                    {review.token}.{" "}
                    {editing.kind === "create"
                      ? "Estimated $0.04/hour."
                      : "Provider identity and capacity pinned."}
                  </p>
                )}
                <CloudError message={error} />
                <DialogFooter>
                  <Button
                    variant="outline"
                    onClick={() => {
                      setGeneration(generation + 1)
                      setError(
                        "Connection changed. Refresh the review before applying."
                      )
                    }}
                  >
                    Simulate stale review
                  </Button>
                  <Button
                    disabled={!name || (editing.kind === "adopt" && !confirmed)}
                    onClick={() =>
                      review
                        ? apply()
                        : (setError(""),
                          setReview({
                            ...editing,
                            name,
                            size,
                            token: generation,
                          }))
                    }
                  >
                    {review ? "Apply reviewed server" : "Review server"}
                  </Button>
                </DialogFooter>
              </>
            )}
          </DialogContent>
        </Dialog>
      )}
      {poolDialog && (
        <Dialog open onOpenChange={setPoolDialog}>
          <DialogContent className="max-h-[90dvh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Pool budgets</DialogTitle>
              <DialogDescription>
                shared-services. Choose member servers and their usable budgets.
              </DialogDescription>
            </DialogHeader>
            {hosts
              .filter((h) => h.status === "ready")
              .map((host) => {
                const member = poolMembers.find((m) => m.serverId === host.id)
                return (
                  <div
                    key={host.id}
                    className="grid gap-2 rounded-lg bg-muted p-3"
                  >
                    <FormField
                      orientation="horizontal"
                      className="flex items-center gap-2 text-sm"
                    >
                      <FieldLabel className="contents">
                        <Checkbox
                          checked={!!member}
                          onCheckedChange={(e) =>
                            setPoolMembers(
                              e
                                ? [
                                    ...poolMembers,
                                    {
                                      serverId: host.id,
                                      cpuMillis: 1000,
                                      memoryMib: 2048,
                                      draining: host.draining,
                                    },
                                  ]
                                : poolMembers.filter(
                                    (m) => m.serverId !== host.id
                                  )
                            )
                          }
                        />
                        {host.name}
                      </FieldLabel>
                    </FormField>
                    {member && (
                      <div className="grid gap-2 sm:grid-cols-2">
                        <Field label="CPU budget (millicores)">
                          <Input
                            aria-label={`CPU budget for ${host.name}`}
                            type="number"
                            min={1}
                            value={member.cpuMillis}
                            onChange={(e) =>
                              setPoolMembers(
                                poolMembers.map((m) =>
                                  m.serverId === host.id
                                    ? {
                                        ...m,
                                        cpuMillis: Number(e.target.value),
                                      }
                                    : m
                                )
                              )
                            }
                          />
                        </Field>
                        <Field label="Memory budget (MiB)">
                          <Input
                            aria-label={`Memory budget for ${host.name}`}
                            type="number"
                            min={1}
                            value={member.memoryMib}
                            onChange={(e) =>
                              setPoolMembers(
                                poolMembers.map((m) =>
                                  m.serverId === host.id
                                    ? {
                                        ...m,
                                        memoryMib: Number(e.target.value),
                                      }
                                    : m
                                )
                              )
                            }
                          />
                        </Field>
                      </div>
                    )}
                  </div>
                )
              })}
            <Button
              disabled={
                !poolMembers.length ||
                poolMembers.some((m) => m.cpuMillis <= 0 || m.memoryMib <= 0)
              }
              onClick={() => {
                setPool(true)
                onPoolsChange?.(["shared-services"])
                setPoolDialog(false)
                record(
                  `shared-services pool saved: ${poolMembers.map((m) => `${m.serverId} ${m.cpuMillis} mCPU / ${m.memoryMib} MiB`).join(", ")}.`
                )
              }}
            >
              Save pool budgets
            </Button>
          </DialogContent>
        </Dialog>
      )}
      {domainDialog && (
        <Dialog open onOpenChange={setDomainDialog}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Add route</DialogTitle>
              <DialogDescription>
                Web app / production / api. DNS authority is independent of
                compute.
              </DialogDescription>
            </DialogHeader>
            <Field label="Hostname">
              <Input
                aria-label="Route hostname"
                value={hostname}
                onChange={(e) => {
                  setHostname(e.target.value)
                  setRouteReview(false)
                }}
              />
            </Field>
            <Field label="DNS mode">
              <Select
                value={mode}
                onValueChange={(e) => {
                  setMode(e ?? "")
                  setRouteReview(false)
                }}
              >
                <SelectTrigger
                  aria-label="DNS mode"
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="external">External DNS</SelectItem>
                  <SelectItem value="managed">
                    Managed DNS, separate account
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {mode === "external" && (
              <Field label="Maximum previously served TTL (seconds)">
                <Input
                  aria-label="Maximum previously served TTL (seconds)"
                  type="number"
                  min={60}
                  max={86400}
                  value={ttl}
                  onChange={(e) => {
                    setTTL(e.target.value)
                    setRouteReview(false)
                  }}
                />
              </Field>
            )}
            {!zoneVerified && (
              <Alert variant="destructive" className="text-xs">
                <AlertDescription>
                  Save and verify the DNS zone before reviewing a route.
                </AlertDescription>
              </Alert>
            )}
            {routeReview && (
              <p className="rounded-lg bg-muted p-3 text-sm">
                A {hostname} = 192.0.2.20 · maximum old TTL {ttl}s. Deployment
                identity and zone authority pinned.
              </p>
            )}
            <Button
              disabled={
                !zoneVerified ||
                !hostname ||
                (mode === "external" && !validExternalTTL(Number(ttl)))
              }
              onClick={() => {
                if (mode === "external" && !validExternalTTL(Number(ttl)))
                  return
                if (!routeReview) {
                  setRouteReview(true)
                  return
                }
                setDomains([
                  ...domains,
                  {
                    hostname,
                    mode,
                    ttl: mode === "external" ? Number(ttl) : 300,
                    status: "pending",
                    dns: false,
                    tls: false,
                    backend: false,
                    revision: 1,
                  },
                ])
                record(
                  `${hostname}: reviewed route accepted. DNS, TLS and backend verification pending.`
                )
                setDomainDialog(false)
              }}
            >
              {routeReview ? "Apply reviewed route" : "Review route"}
            </Button>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}
