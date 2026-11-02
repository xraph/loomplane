import { DatabaseIcon, RefreshCwIcon } from "lucide-react"
import { useState } from "react"
import type { Connection } from "@/client/forge/loomplane/types"
import { CloudGlyph } from "@/components/cloud-glyph"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { ProductIcon } from "@/components/product-icon"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import {
  filterProviderResources,
  providerResourceReadiness,
  type InventoryResource,
} from "@/lib/provider-resources"

export function ResourceInventory({
  resources,
  connections,
  now,
  onSelect,
  onSync,
  connection = "",
  usage,
  usageLoaded,
  usageFailed,
  canSync = true,
}: {
  resources: InventoryResource[]
  connections: (Pick<Connection, "id" | "name"> &
    Partial<Pick<Connection, "cloud" | "accountIdentity">>)[]
  now: number
  onSelect: (resource: InventoryResource) => void
  onSync: () => void
  connection?: string
  usage?: Record<string, number>
  usageLoaded?: boolean
  usageFailed?: boolean
  canSync?: boolean
}) {
  const [search, setSearch] = useState("")
  const [ownership, setOwnership] = useState("all")
  const [account, setAccount] = useState(connection || "all")
  const [kind, setKind] = useState("all")
  const selectedAccount = connections.find((c) => c.id === account)
  const filtered = filterProviderResources(resources, {
    search,
    ownership,
    connection: account,
    accountIdentity: selectedAccount?.accountIdentity,
    provider: selectedAccount?.cloud,
    kind,
  })
  const filteredOut =
    resources.length > 0 ||
    !!search ||
    ownership !== "all" ||
    account !== "all" ||
    kind !== "all"
  const reset = () => {
    setSearch("")
    setOwnership("all")
    setAccount("all")
    setKind("all")
  }
  return (
    <section aria-label="Resource inventory" className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Tabs
          value={ownership}
          onValueChange={(value) => setOwnership(String(value))}
        >
          <TabsList aria-label="Resource ownership">
            <TabsTrigger value="all">All</TabsTrigger>
            <TabsTrigger value="external">External</TabsTrigger>
            <TabsTrigger value="loomplane">Loomplane-created</TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            aria-label="Search resources"
            placeholder="Search resources…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            className="h-7 w-48 text-[13px]"
          />
          <Select
            value={account}
            onValueChange={(value) => setAccount(value ?? "all")}
          >
            <SelectTrigger size="sm" aria-label="Account">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All accounts</SelectItem>
              {connections.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={kind}
            onValueChange={(value) => setKind(value ?? "all")}
          >
            <SelectTrigger size="sm" aria-label="Resource kind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All kinds</SelectItem>
              {[...new Set(resources.map((r) => r.kind))].sort().map((k) => (
                <SelectItem key={k} value={k}>
                  {k}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      {filtered.length === 0 ? (
        <ZeroState variant="compact">
          <ZeroStateHeader>
            <ZeroStateMedia variant="icon">
              <DatabaseIcon />
            </ZeroStateMedia>
            <ZeroStateTitle>
              {filteredOut
                ? "No matching resources"
                : "Sync your first account"}
            </ZeroStateTitle>
            <ZeroStateDescription>
              {filteredOut
                ? "Try another filter to find your resource."
                : "Discover databases, caches and buckets in your connected accounts. Your credentials stay in Vault."}
            </ZeroStateDescription>
          </ZeroStateHeader>
          <ZeroStateContent>
            {filteredOut ? (
              <Button size="sm" variant="outline" onClick={reset}>
                Clear filters
              </Button>
            ) : (
              <Button size="sm" onClick={onSync} disabled={!canSync}>
                <RefreshCwIcon />
                Sync accounts
              </Button>
            )}
          </ZeroStateContent>
        </ZeroState>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Resource</TableHead>
                <TableHead className="hidden sm:table-cell">
                  Provider / account
                </TableHead>
                <TableHead className="hidden md:table-cell">Region</TableHead>
                <TableHead className="hidden lg:table-cell">
                  Provider health
                </TableHead>
                <TableHead>Connection</TableHead>
                <TableHead className="hidden 2xl:table-cell">
                  Ownership
                </TableHead>
                <TableHead className="hidden lg:table-cell">Usage</TableHead>
                <TableHead className="hidden sm:table-cell">
                  Last seen
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className="[&_td]:py-1.5">
              {filtered.map((r) => {
                const readiness = providerResourceReadiness(r, now)
                const name =
                  connections.find((c) => c.id === r.connectionId)?.name ??
                  r.accountIdentity
                return (
                  <TableRow key={r.id} className="h-12">
                    <TableCell className="py-1.5">
                      <div className="flex items-center gap-2">
                        <ProductIcon
                          product={
                            r.kind === "postgres"
                              ? "postgresql"
                              : r.kind === "mysql"
                                ? "mysql"
                                : r.kind === "redis"
                                  ? "redis"
                                  : undefined
                          }
                          fallback={DatabaseIcon}
                          size="sm"
                        />
                        <div className="min-w-0">
                          <Button
                            size="sm"
                            variant="link"
                            className="h-auto max-w-40 justify-start truncate p-0 text-[13px]"
                            onClick={() => onSelect(r)}
                          >
                            {r.name}
                          </Button>
                          <div className="text-xs text-muted-foreground">
                            {r.kind}
                            {r.version ? ` ${r.version}` : ""}
                          </div>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="hidden py-1.5 sm:table-cell">
                      <div className="flex items-center gap-1.5">
                        <CloudGlyph cloud={r.provider} />
                        <span>{cloudLabel(r.provider)}</span>
                      </div>
                      <div
                        className="max-w-44 truncate text-xs text-muted-foreground"
                        title={name}
                      >
                        {name}
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {r.region || "Unspecified"}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <StatusPill
                        tone={
                          r.status === "missing" || r.status === "failed"
                            ? "failed"
                            : "quiet"
                        }
                      >
                        {r.status || "Unknown"}
                      </StatusPill>
                    </TableCell>
                    <TableCell>
                      <StatusPill tone={r.endpointId ? "quiet" : "waiting"}>
                        {readiness.credentials}
                      </StatusPill>
                      {readiness.discovery !== "Observed" && (
                        <div className="mt-1 text-xs text-muted-foreground sm:hidden">
                          {readiness.discovery}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="hidden 2xl:table-cell">
                      {r.ownershipState === "unverified"
                        ? "Unverified"
                        : r.ownership === "external"
                          ? "External"
                          : "Loomplane"}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      {usageFailed
                        ? "Unavailable"
                        : !usageLoaded
                          ? "Loading…"
                          : `${usage?.[r.id] ?? 0} ${(usage?.[r.id] ?? 0) === 1 ? "deployment" : "deployments"}`}
                    </TableCell>
                    <TableCell className="hidden sm:table-cell">
                      <div title={formatDate(r.observedAt)}>
                        {relative(new Date(r.observedAt).getTime(), now)}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {readiness.discovery}
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {filtered.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {filtered.length} of {resources.length} resources. Credentials and
          network access are checked separately.
        </p>
      )}
    </section>
  )
}
