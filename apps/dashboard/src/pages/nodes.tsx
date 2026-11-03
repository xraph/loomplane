import { PlusIcon, ServerIcon } from "lucide-react"
import { useState } from "react"

import { useNodesList } from "@/client/forge/loomplane/hooks"
import type { Node } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { AddNode } from "@/components/nodes/add-node"
import { NodeStatus } from "@/components/nodes/node-status"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
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
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { date, relative } from "@/lib/format"
import {
  endpointBadges,
  labelList,
  machineLabel,
  roleNote,
  trafficLabel,
} from "@/lib/nodes"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/** Nodes lists the machines that joined the organization. */
export function Nodes() {
  const { can } = useOrganization()
  const manage = can("nodes:manage")
  const nodes = useQuery(useNodesList)
  const [adding, setAdding] = useState(false)
  const list = nodes.data?.nodes ?? []
  const count = (status: string) =>
    list.filter((n) => n.status === status).length

  const add = manage ? (
    <Button onClick={() => setAdding(true)}>
      <PlusIcon />
      Add node
    </Button>
  ) : undefined

  return (
    <>
      <SiteHeader crumbs={[{ label: "Nodes" }]} />
      <Page>
        <PageHeading
          title="Nodes"
          description="Machines that joined your organization. Each one dials out to Loomplane, so it needs no open ports."
          actions={add}
        />

        {nodes.status === "error" && (
          <QueryError
            error={nodes.error}
            fallback="Nodes couldn't be loaded."
            retry={nodes.refetch}
          />
        )}
        {!nodes.data && nodes.status !== "error" && (
          <Skeleton className="h-48 w-full rounded-xl" />
        )}
        {nodes.data && list.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ServerIcon />
              </EmptyMedia>
              <EmptyTitle>No nodes yet</EmptyTitle>
              <EmptyDescription>
                Join any Linux machine with one command. No open ports.
              </EmptyDescription>
            </EmptyHeader>
            {add && <EmptyContent>{add}</EmptyContent>}
          </Empty>
        )}
        {nodes.data && list.length > 0 && (
          <>
            <div className="grid gap-3 sm:grid-cols-3">
              <Tile label="Online" value={count("online")} />
              <Tile label="Offline" value={count("offline")} />
              <Tile label="Waiting to connect" value={count("pending")} />
            </div>
            <NodeTable nodes={list} />
          </>
        )}
      </Page>
      {manage && <AddNode open={adding} onOpenChange={setAdding} />}
    </>
  )
}

function Tile({ label, value }: { label: string; value: number }) {
  return (
    <Card className="gap-1 px-4 py-3">
      <span className="text-[12.5px] text-muted-foreground">{label}</span>
      <span className="font-mono text-2xl font-semibold tabular-nums">
        {value}
      </span>
    </Card>
  )
}

function NodeTable({ nodes }: { nodes: Node[] }) {
  const now = useNow(30_000)
  return (
    <Card className="py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Name</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Endpoints</TableHead>
            <TableHead className="hidden lg:table-cell">Machine</TableHead>
            <TableHead className="hidden md:table-cell">Traffic</TableHead>
            <TableHead className="hidden md:table-cell">Version</TableHead>
            <TableHead className="hidden md:table-cell">Last seen</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {nodes.map((n) => (
            <NodeRow key={n.id} node={n} now={now} />
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

function NodeRow({ node: n, now }: { node: Node; now: number }) {
  const pending = n.status === "pending"
  const revoked = n.status === "revoked"
  const badges = endpointBadges(n)
  const labels = labelList(n)
  const seen = n.lastSeenAt ? new Date(n.lastSeenAt).getTime() : 0
  return (
    <TableRow className={cn(revoked && "opacity-60")}>
      <TableCell>
        <Link
          to="/nodes/$id"
          params={{ id: n.id }}
          className="text-[13.5px] font-medium hover:underline"
        >
          {n.name}
        </Link>
        <div className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
          {pending
            ? `Joined ${relative(new Date(n.createdAt).getTime(), now)}`
            : revoked && n.revokedAt
              ? `Revoked ${date(new Date(n.revokedAt).getTime())}`
              : [
                  roleNote(n) && <span key="note">{roleNote(n)}</span>,
                  ...(roleNote(n) ? [] : labels).map((l) => (
                    <span key={l} className="font-mono">
                      {l}
                    </span>
                  )),
                ]}
        </div>
      </TableCell>
      <TableCell>
        <NodeStatus node={n} />
      </TableCell>
      <TableCell>
        {pending ? (
          <span className="text-xs text-muted-foreground">
            Set when it connects
          </span>
        ) : badges.length === 0 ? (
          <span className="text-xs text-muted-foreground">None</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {badges.map((b) => (
              <Badge key={b} variant="secondary" className="font-mono">
                {b}
              </Badge>
            ))}
          </div>
        )}
      </TableCell>
      <TableCell className="hidden font-mono text-xs lg:table-cell">
        {pending ? (
          <span className="font-sans text-muted-foreground">
            Not reported yet
          </span>
        ) : (
          machineLabel(n)
        )}
      </TableCell>
      <TableCell className="hidden text-[12.5px] md:table-cell">
        {trafficLabel(n)}
      </TableCell>
      <TableCell className="hidden font-mono text-xs md:table-cell">
        {n.version || (
          <span className="font-sans text-muted-foreground">
            Not reported yet
          </span>
        )}
      </TableCell>
      <TableCell className="hidden text-[12.5px] text-muted-foreground md:table-cell">
        {!seen
          ? "Never"
          : n.status === "online"
            ? relative(seen, now)
            : date(seen)}
      </TableCell>
    </TableRow>
  )
}
