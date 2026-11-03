import { useState, type FormEvent, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useNodesGet,
  useNodesList,
  useNodesRevoke,
  useNodesUpdate,
} from "@/client/forge/loomplane/hooks"
import type { Node } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { NodeStatus } from "@/components/nodes/node-status"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { formatDate } from "@/lib/format"
import { endpointBadges, gpuLabel, labelList } from "@/lib/nodes"
import { useOrganization } from "@/lib/organization"

/** NodePage is one node: what Loomplane may use on it, who it is and what it runs on. */
export function NodePage({ id }: { id: string }) {
  const { can } = useOrganization()
  const manage = can("nodes:manage")
  const node = useQuery(useNodesGet, { path: { id } })
  const n = node.data
  const [dialog, setDialog] = useState<"rename" | "narrow" | "revoke">()
  const live = !!n && n.status !== "revoked"
  const labels = n ? labelList(n) : []

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Nodes", to: "/nodes" },
          { label: n?.name ?? "Node" },
        ]}
      />
      <Page className="max-w-[980px]">
        {node.status === "error" && (
          <QueryError
            error={node.error}
            fallback="This node couldn't be loaded."
            retry={node.refetch}
          />
        )}
        {!n && node.status !== "error" && (
          <Skeleton className="h-64 w-full rounded-xl" />
        )}
        {n && (
          <>
            <PageHeading
              title={
                <span className="flex flex-wrap items-center gap-2.5">
                  {n.name}
                  <NodeStatus node={n} />
                </span>
              }
              description={
                labels.length > 0 && (
                  <span className="flex flex-wrap gap-1.5">
                    {labels.map((l) => (
                      <Badge key={l} variant="secondary" className="font-mono">
                        {l}
                      </Badge>
                    ))}
                  </span>
                )
              }
              actions={
                manage && live ? (
                  <>
                    <Button
                      variant="outline"
                      onClick={() => setDialog("rename")}
                    >
                      Rename
                    </Button>
                    <Button
                      variant="outline"
                      onClick={() => setDialog("narrow")}
                    >
                      Narrow endpoints
                    </Button>
                    <Button
                      variant="outline"
                      className="text-destructive hover:text-destructive"
                      onClick={() => setDialog("revoke")}
                    >
                      Revoke
                    </Button>
                  </>
                ) : undefined
              }
            />
            <div className="grid gap-4 md:grid-cols-2">
              <Allowed node={n} />
              <Identity node={n} />
              <Machine node={n} />
              <Release node={n} />
            </div>
            <Section title="What runs here">
              <p className="text-[13px] text-muted-foreground">
                Nothing yet. Once deploying to nodes arrives, the services,
                databases and domains on this machine show here.
              </p>
            </Section>
            {dialog === "rename" && (
              <RenameDialog node={n} onClose={() => setDialog(undefined)} />
            )}
            {dialog === "narrow" && (
              <NarrowDialog node={n} onClose={() => setDialog(undefined)} />
            )}
            {dialog === "revoke" && (
              <RevokeDialog node={n} onClose={() => setDialog(undefined)} />
            )}
          </>
        )}
      </Page>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card className="content-start gap-3 px-4 py-4">
      <h2 className="text-[13.5px] font-semibold">{title}</h2>
      {children}
    </Card>
  )
}

function Facts({ children }: { children: ReactNode }) {
  return (
    <dl className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
      {children}
    </dl>
  )
}

function Fact({
  label,
  mono,
  children,
}: {
  label: string
  mono?: boolean
  children: ReactNode
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "font-mono break-all" : "break-words"}>
        {children}
      </dd>
    </>
  )
}

function Allowed({ node: n }: { node: Node }) {
  const badges = endpointBadges(n)
  return (
    <Section title="What Loomplane may use">
      <div className="flex flex-wrap gap-1.5">
        {badges.length === 0 ? (
          <span className="text-[13px] text-muted-foreground">Nothing</span>
        ) : (
          badges.map((b) => (
            <Badge key={b} variant="secondary" className="font-mono">
              {b}
            </Badge>
          ))
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        You can take endpoints away here. Adding one needs root on the machine:
      </p>
      <code className="block rounded-lg bg-muted px-3 py-2 font-mono text-xs">
        sudo loomctl node allow dial
      </code>
      {badges.includes("docker") && (
        <p className="text-xs text-muted-foreground">
          Access to the Docker socket is root access on this machine.
        </p>
      )}
    </Section>
  )
}

function Identity({ node: n }: { node: Node }) {
  return (
    <Section title="Identity">
      <Facts>
        <Fact label="Key fingerprint" mono>
          {n.keyFingerprint}
        </Fact>
        <Fact label="Identity">Generation {n.identityGeneration}</Fact>
        <Fact label="Joined">
          {formatDate(n.createdAt)}
          {n.joinedByKeyId && (
            <>
              , with the API key{" "}
              <span className="font-mono">{n.joinedByKeyId}</span>
            </>
          )}
        </Fact>
      </Facts>
    </Section>
  )
}

function Machine({ node: n }: { node: Node }) {
  const i = n.inventory
  const reported = !!i && !!(i.os || i.arch || i.cpus > 0)
  const runtime = [
    i?.docker && `Docker ${i.docker.version}`,
    i?.podman && `Podman ${i.podman.version}`,
  ]
    .filter(Boolean)
    .join(", ")
  const gpus = gpuLabel(n)
  const addresses = i?.publicAddresses ?? []
  return (
    <Section title="Machine">
      {!reported ? (
        <p className="text-[13px] text-muted-foreground">
          Not reported yet. The machine sends this when it connects.
        </p>
      ) : (
        <Facts>
          <Fact label="System">
            {[i.os, i.kernel, i.arch].filter(Boolean).join(", ")}
          </Fact>
          <Fact label="CPU and memory" mono>
            {i.cpus} CPU · {Math.round(i.memoryMib / 1024)} GiB
          </Fact>
          {i.diskGib > 0 && (
            <Fact label="Disk" mono>
              {i.diskGib} GiB
            </Fact>
          )}
          <Fact label="Runtime">
            {runtime || (
              <span className="text-muted-foreground">None found</span>
            )}
          </Fact>
          <Fact label="GPUs">
            {gpus || <span className="text-muted-foreground">None</span>}
          </Fact>
          {i.ports80443 && (
            <Fact label="Ports 80 and 443">
              {i.ports80443 === "free"
                ? "Free"
                : i.ports80443 === "busy"
                  ? "In use"
                  : i.ports80443}
            </Fact>
          )}
          <Fact label="Public address" mono={addresses.length > 0}>
            {addresses.length > 0 ? (
              addresses.join(", ")
            ) : (
              <span className="font-sans text-muted-foreground">
                None reported
              </span>
            )}
          </Fact>
        </Facts>
      )}
    </Section>
  )
}

function Release({ node: n }: { node: Node }) {
  return (
    <Section title="Release">
      <Facts>
        <Fact label="Version" mono>
          {n.version || "Not reported yet"}
        </Fact>
        <Fact label="Protocol">{n.protocolVersion}</Fact>
        {n.version && (
          <Fact label="Updates">
            Run{" "}
            <span className="font-mono">
              sudo loomctl node update --release {n.version}
            </span>{" "}
            on the machine
          </Fact>
        )}
      </Facts>
    </Section>
  )
}

function RenameDialog({
  node: n,
  onClose,
}: {
  node: Node
  onClose: () => void
}) {
  const update = useMutation(useNodesUpdate)
  const invalidate = useInvalidate()
  const [name, setName] = useState(n.name)
  const [error, setError] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await update.mutateAsync({
        path: { id: n.id },
        body: { name: name.trim() },
      })
      invalidate(useNodesList)
      toast.success("Node renamed")
      onClose()
    } catch (err) {
      setError(messageOf(err, "The node wasn't renamed."))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[440px]">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Rename {n.name}</DialogTitle>
            <DialogDescription>
              The name is only for you. The machine keeps its identity.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <label htmlFor="node-name" className="text-[13px] font-medium">
              Name
            </label>
            <Input
              id="node-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={80}
              required
            />
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                update.isPending || !name.trim() || name.trim() === n.name
              }
            >
              {update.isPending && <Spinner />}
              Rename
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function NarrowDialog({
  node: n,
  onClose,
}: {
  node: Node
  onClose: () => void
}) {
  const update = useMutation(useNodesUpdate)
  const invalidate = useInvalidate()
  const [kept, setKept] = useState<string[]>(endpointBadges(n))
  const [error, setError] = useState("")
  const dropped = endpointBadges(n).filter((e) => !kept.includes(e))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      await update.mutateAsync({
        path: { id: n.id },
        body: { endpoints: kept },
      })
      invalidate(useNodesList)
      toast.success("Endpoints narrowed")
      onClose()
    } catch (err) {
      setError(messageOf(err, "The endpoints weren't narrowed."))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[460px]">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Narrow endpoints</DialogTitle>
            <DialogDescription>
              Untick what Loomplane should stop using on {n.name}. Adding one
              back needs root on the machine:{" "}
              <code className="font-mono text-xs">
                sudo loomctl node allow &lt;endpoint&gt;
              </code>
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            {endpointBadges(n).map((ep) => (
              <label key={ep} className="flex items-center gap-2 text-[13px]">
                <input
                  type="checkbox"
                  checked={kept.includes(ep)}
                  onChange={(e) =>
                    setKept((cur) =>
                      e.target.checked
                        ? [...cur, ep]
                        : cur.filter((x) => x !== ep)
                    )
                  }
                  className="size-4 accent-[var(--primary)]"
                />
                <span className="font-mono">{ep}</span>
              </label>
            ))}
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={update.isPending || dropped.length === 0}
            >
              {update.isPending && <Spinner />}
              Take away
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function RevokeDialog({
  node: n,
  onClose,
}: {
  node: Node
  onClose: () => void
}) {
  const revoke = useMutation(useNodesRevoke)
  const invalidate = useInvalidate()
  const [error, setError] = useState("")

  const confirm = async () => {
    setError("")
    try {
      await revoke.mutateAsync({ path: { id: n.id } })
      invalidate(useNodesList)
      toast.success(`${n.name} is revoked`)
      onClose()
    } catch (err) {
      setError(messageOf(err, "The node wasn't revoked."))
    }
  }

  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke {n.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            It disconnects at once and can't connect again. You can't undo this;
            to use the machine again, join it with a new token.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <Button
            variant="destructive"
            disabled={revoke.isPending}
            onClick={() => void confirm()}
          >
            {revoke.isPending && <Spinner />}
            Revoke
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
