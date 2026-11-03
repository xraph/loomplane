import { CheckIcon } from "lucide-react"
import { useRef, useState, type ReactNode } from "react"

import { messageOf } from "@/client/errors"
import {
  useNodeJoinTokensCreate,
  useNodesList,
  useOrganizationCloudsList,
} from "@/client/forge/loomplane/hooks"
import type { JoinToken, Node } from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
import { CopyButton } from "@/components/runs"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  clockTime,
  gpuLabel,
  hasPublicAddress,
  joinedNode,
  parseLabels,
} from "@/lib/nodes"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

const endpointChoices = [
  {
    value: "docker",
    note: "Docker or Podman socket; root access on the machine",
  },
  { value: "probe", note: "reports CPU, memory, disk and GPUs" },
  { value: "dial", note: "connections to local services" },
]

const NO_CLOUD = "none"

/** AddNode is the right-hand panel that joins a machine as a node. */
export function AddNode({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-lg">
        <Panel onClose={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}

type Minted = {
  token: JoinToken
  /** The nodes already in the list when the token was made. */
  before: Set<string>
  summary: string
}

function Panel({ onClose }: { onClose: () => void }) {
  const { session } = useOrganization()
  const nodes = useQuery(useNodesList)
  const clouds = useQuery(useOrganizationCloudsList)
  const create = useMutation(useNodeJoinTokensCreate)
  const [cloud, setCloud] = useState(NO_CLOUD)
  const [labelsText, setLabelsText] = useState("")
  const [endpoints, setEndpoints] = useState<string[]>(["docker", "probe"])
  const [minted, setMinted] = useState<Minted>()
  const [error, setError] = useState("")
  // A fresh start for "Add another".
  const [round, setRound] = useState(0)
  const busy = useRef(false)

  const cloudList = (clouds.data?.clouds ?? []).flatMap((c) => (c ? [c] : []))
  const waiting = !!minted
  const joined = minted
    ? joinedNode(nodes.data?.nodes ?? [], minted.before, session.userId)
    : undefined
  // The stream tells the list when a node changes. This is the fallback while
  // it is down, and it stops once the machine has connected.
  useRefetchWhile(waiting && !joined, nodes.refetch)

  const submit = async () => {
    if (busy.current || !nodes.data) return
    const parsed = parseLabels(labelsText)
    if (!parsed.ok) {
      setError(parsed.error)
      return
    }
    busy.current = true
    setError("")
    try {
      const before = new Set(nodes.data.nodes.map((n) => n.id))
      const token = (await create.mutateAsync({
        body: {
          ...(cloud !== NO_CLOUD && { cloudId: cloud }),
          ...(Object.keys(parsed.labels).length > 0 && {
            labels: parsed.labels,
          }),
          endpoints,
        },
      })) as JoinToken
      const cloudName = cloudList.find((c) => c.id === cloud)?.name
      setMinted({
        token,
        before,
        summary: [
          cloudName ? `Cloud ${cloudName}` : "",
          Object.entries(parsed.labels)
            .map(([k, v]) => `${k}=${v}`)
            .join(", "),
          endpoints.join(", "),
        ]
          .filter(Boolean)
          .join(" · "),
      })
    } catch (err) {
      setError(messageOf(err, "The token wasn't made."))
    } finally {
      busy.current = false
    }
  }

  const reset = () => {
    setMinted(undefined)
    setLabelsText("")
    setCloud(NO_CLOUD)
    setEndpoints(["docker", "probe"])
    setError("")
    setRound((r) => r + 1)
  }

  if (minted && joined) {
    return (
      <Connected
        node={joined}
        summary={minted.summary}
        onClose={onClose}
        onAnother={reset}
      />
    )
  }

  const locked = !!minted
  return (
    <div key={round} className="grid gap-5 p-4 pt-4">
      <SheetHeader className="p-0 pr-8">
        <SheetTitle className="text-lg font-semibold">Add node</SheetTitle>
        <SheetDescription>
          Run one command on the machine. It dials out to Loomplane, so it can
          sit behind a router or a firewall.
        </SheetDescription>
      </SheetHeader>

      <Step n={1} title="Choose what the token allows">
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <label htmlFor="node-cloud" className="text-[13px] font-medium">
              Cloud
            </label>
            <Select
              items={[
                ...cloudList.map((c) => ({ label: c.name, value: c.id })),
                { label: "None for now", value: NO_CLOUD },
              ]}
              value={cloud}
              onValueChange={(v) => setCloud(v ?? NO_CLOUD)}
              disabled={locked}
            >
              <SelectTrigger id="node-cloud" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {cloudList.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
                <SelectItem value={NO_CLOUD}>None for now</SelectItem>
              </SelectContent>
            </Select>
            <span className="text-xs text-muted-foreground">
              Recorded now. The node joins the Cloud once deploying to nodes
              arrives.
            </span>
          </div>
          <div className="grid gap-1.5">
            <label htmlFor="node-labels" className="text-[13px] font-medium">
              Labels
            </label>
            <Input
              id="node-labels"
              value={labelsText}
              onChange={(e) => setLabelsText(e.target.value)}
              placeholder="region=home"
              disabled={locked}
              className="font-mono"
            />
          </div>
          <fieldset className="grid gap-2" disabled={locked}>
            <legend className="mb-1 text-[13px] font-medium">
              What Loomplane may use
            </legend>
            {endpointChoices.map((c) => (
              <label
                key={c.value}
                className="flex items-start gap-2 text-[13px]"
              >
                <input
                  type="checkbox"
                  checked={endpoints.includes(c.value)}
                  onChange={(e) =>
                    setEndpoints((cur) =>
                      e.target.checked
                        ? [...cur, c.value]
                        : cur.filter((x) => x !== c.value)
                    )
                  }
                  className="mt-0.5 size-4 accent-[var(--primary)]"
                />
                <span>
                  <span className="font-mono">{c.value}</span>{" "}
                  <span className="text-muted-foreground">({c.note})</span>
                </span>
              </label>
            ))}
          </fieldset>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {nodes.status === "error" && (
            <Alert variant="destructive">
              <AlertDescription>
                {messageOf(nodes.error, "Nodes couldn't be loaded.")}
              </AlertDescription>
            </Alert>
          )}
          {!locked && (
            <div>
              <Button
                onClick={() => void submit()}
                disabled={
                  create.isPending || !nodes.data || endpoints.length === 0
                }
              >
                {create.isPending && <Spinner />}
                Make the token
              </Button>
            </div>
          )}
        </div>
      </Step>

      <Step n={2} title="Run this on the machine" dim={!minted}>
        {minted ? (
          <div className="grid gap-2">
            <div className="flex items-start gap-1 rounded-lg bg-subtle py-1.5 pr-1 pl-2.5 ring-1 ring-border">
              <code className="min-w-0 flex-1 font-mono text-[11.5px] leading-relaxed break-all">
                {minted.token.command}
              </code>
              <CopyButton value={minted.token.command} label="Copy command" />
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">
              The token works once and expires at{" "}
              {clockTime(minted.token.expiresAt)}. The script checks the release
              signature before it installs anything.
            </p>
            <p className="text-xs leading-relaxed text-muted-foreground">
              Logged in with loomctl already? Run{" "}
              <code className="font-mono">sudo loomctl node join</code> on
              Linux, or <code className="font-mono">loomctl node join</code> on
              a Mac, instead. It makes its own token from your login and asks
              what to allow, so the choices above don&apos;t apply.
            </p>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            The command appears here once the token is made.
          </p>
        )}
      </Step>

      <Step n={3} title="Wait for it to connect" dim={!minted}>
        {minted && (
          <div
            role="status"
            className="flex items-center gap-2 text-[13px] text-muted-foreground"
          >
            <Spinner />
            Waiting for the machine to connect…
          </div>
        )}
      </Step>
    </div>
  )
}

function Step({
  n,
  title,
  dim,
  children,
}: {
  n: number
  title: string
  dim?: boolean
  children?: ReactNode
}) {
  return (
    <section
      className={
        dim
          ? "grid grid-cols-[1.5rem_minmax(0,1fr)] gap-3 opacity-60"
          : "grid grid-cols-[1.5rem_minmax(0,1fr)] gap-3"
      }
    >
      <span className="flex size-6 items-center justify-center rounded-full border font-mono text-xs text-muted-foreground">
        {n}
      </span>
      <div className="grid min-w-0 gap-2">
        <h3 className="text-[13.5px] font-medium">{title}</h3>
        {children}
      </div>
    </section>
  )
}

function Done({ title, note }: { title: string; note: string }) {
  return (
    <section className="grid grid-cols-[1.5rem_minmax(0,1fr)] gap-3">
      <span className="flex size-6 items-center justify-center rounded-full bg-overdye-tint text-overdye-ink">
        <CheckIcon className="size-3.5" aria-hidden="true" />
      </span>
      <div className="grid gap-0.5">
        <h3 className="text-[13.5px] font-medium">{title}</h3>
        <p className="text-xs text-muted-foreground">{note}</p>
      </div>
    </section>
  )
}

function Connected({
  node: n,
  summary,
  onClose,
  onAnother,
}: {
  node: Node
  summary: string
  onClose: () => void
  onAnother: () => void
}) {
  const i = n.inventory
  const runtime = i?.docker
    ? `Docker ${i.docker.version}`
    : i?.podman
      ? `Podman ${i.podman.version}`
      : ""
  const gpus = gpuLabel(n)
  const machine = [i?.os, i?.kernel, i?.arch].filter(Boolean).join(", ")
  return (
    <div className="grid gap-5 p-4 pt-4">
      <SheetHeader className="p-0 pr-8">
        <SheetTitle className="text-lg font-semibold">Add node</SheetTitle>
      </SheetHeader>
      <Done title="Token scoped" note={summary || "No scope chosen"} />
      <Done
        title="Command ran on the machine"
        note="The token is spent and can't be used again."
      />

      <div
        role="status"
        className="grid gap-3 rounded-xl bg-overdye-tint/60 p-4 ring-1 ring-border"
      >
        <div className="flex items-center gap-2 text-[14px] font-semibold">
          <span className="size-2 rounded-full bg-overdye" aria-hidden="true" />
          {n.name} is {n.status === "online" ? "online" : "connected"}
        </div>
        <dl className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
          {machine && <Row label="Machine">{machine}</Row>}
          {i && i.cpus > 0 && (
            <Row label="CPU and memory" mono>
              {i.cpus} CPU · {Math.round(i.memoryMib / 1024)} GiB
            </Row>
          )}
          {runtime && <Row label="Runtime">{runtime}</Row>}
          {gpus && <Row label="GPUs">{gpus}</Row>}
          <Row label="Public address" mono={hasPublicAddress(n)}>
            {hasPublicAddress(n) ? (
              (i?.publicAddresses ?? []).join(", ")
            ) : (
              <span className="text-muted-foreground">Not checked yet</span>
            )}
          </Row>
          <Row label="Allowed">
            <span className="flex flex-wrap gap-1">
              {n.endpoints.map((e) => (
                <span
                  key={e}
                  className="rounded-md bg-muted px-1.5 font-mono text-xs"
                >
                  {e}
                </span>
              ))}
            </span>
          </Row>
        </dl>
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          nativeButton={false}
          render={<Link to="/nodes/$id" params={{ id: n.id }} />}
          onClick={onClose}
        >
          Open node
        </Button>
        <Button variant="outline" onClick={onAnother}>
          Add another
        </Button>
      </div>
    </div>
  )
}

function Row({
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
      <dd className={mono ? "font-mono" : undefined}>{children}</dd>
    </>
  )
}
