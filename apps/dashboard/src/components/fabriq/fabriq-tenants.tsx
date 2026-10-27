// The Tenants section: each tenant's rows are kept apart in one database,
// and the services that default to it.
import { useOrganization } from "@/lib/organization"
import { FabriqEmpty } from "./fabriq-empty"
import { PlusIcon } from "lucide-react"
import { useState, type FormEvent } from "react"

import { messageOf } from "@/client/errors"
import {
  useFabriqTenantsCreate,
  useFabriqTenantsList,
  useManifestsCurrent,
} from "@/client/forge/loomplane/hooks"
import { useMutation, useQuery } from "@/client/hooks"
import { FabriqError } from "@/components/fabriq/fabriq-error"
import { OpenInAdmin } from "@/components/fabriq/open-admin-button"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { createdLabel, fabriqNeeds, validTenantId } from "@/lib/fabriq"
import { useNow } from "@/lib/use-runs"

/** FabriqTenants lists a hosted Fabriq's tenants and creates new ones. */
export function FabriqTenants({
  projectId,
  environmentId,
  resource,
}: CatalogPanelProps) {
  const { can } = useOrganization()
  const path = { projectId, environmentId, resource }
  const now = useNow(30_000)
  const tenants = useQuery(useFabriqTenantsList, { path })
  const manifest = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const create = useMutation(useFabriqTenantsCreate)
  const [adding, setAdding] = useState(false)
  const [id, setId] = useState("")
  const [error, setError] = useState("")

  const needs = fabriqNeeds(manifest.data?.manifest?.services, resource)
  // Which services default to a tenant is the manifest's to say, so
  // nothing is claimed until it has been read.
  const defaultFor = (tenant: string) => {
    if (!manifest.data) return manifest.status === "error" ? "Unknown" : ""
    const services = needs.filter((n) => n.tenant === tenant)
    return services.length > 0
      ? services.map((n) => n.service).join(", ")
      : "None"
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!can("deployment:create") || !validTenantId(id) || create.isPending)
      return
    setError("")
    try {
      await create.mutateAsync({ path, body: { tenantId: id } })
      setId("")
      setAdding(false)
    } catch (cause) {
      setError(messageOf(cause, "The tenant wasn't created."))
    }
  }

  return (
    <section aria-label="Tenants" className="grid gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="grid gap-0.5">
          <h2 className="text-[15px] font-semibold">Tenants</h2>
          <span className="text-[12.5px] text-muted-foreground">
            Each tenant&apos;s rows are kept apart by row-level security in one
            database.
          </span>
        </div>
        {tenants.status !== "error" &&
          !!tenants.data?.tenants.length &&
          can("deployment:create") && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-expanded={adding}
              onClick={() => setAdding(!adding)}
            >
              <PlusIcon data-icon="inline-start" aria-hidden />
              New tenant
            </Button>
          )}
      </div>
      {adding && (
        <form
          onSubmit={(event) => void submit(event)}
          className="flex flex-wrap items-end gap-2.5 rounded-xl bg-subtle p-3 ring-1 ring-border"
        >
          <label className="grid flex-[1_1_200px] gap-1.5 text-[12.5px] font-medium">
            Tenant ID
            <Input
              value={id}
              onChange={(event) => setId(event.target.value)}
              placeholder="umbrella"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={id !== "" && !validTenantId(id)}
              className="bg-background font-mono"
            />
          </label>
          <span className="flex-[1_1_220px] text-xs text-muted-foreground">
            Lowercase letters, digits and dashes. Apps use it in the DSN and in
            requests.
          </span>
          <Button
            type="submit"
            disabled={!validTenantId(id) || create.isPending}
          >
            Create tenant
          </Button>
          {error && (
            <Alert variant="destructive" className="basis-full">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </form>
      )}
      {tenants.status === "error" ? (
        <FabriqError
          error={tenants.error}
          fallback="Fabriq's tenants couldn't be loaded."
          retry={tenants.refetch}
        />
      ) : !tenants.data ? (
        <Skeleton
          role="status"
          aria-label="Loading tenants"
          className="h-24 rounded-xl"
        />
      ) : tenants.data.tenants.length === 0 ? (
        <FabriqEmpty
          title="No tenants yet"
          description="Create a tenant to keep its rows apart in this database."
        >
          {can("deployment:create") && (
            <Button size="sm" onClick={() => setAdding(true)}>
              New tenant
            </Button>
          )}
        </FabriqEmpty>
      ) : (
        <Card className="py-0">
          <Table className="min-w-[640px]">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Tenant</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Default for</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tenants.data.tenants.map((tenant) => (
                <TableRow key={tenant.id}>
                  <TableCell className="font-mono font-medium">
                    {tenant.id}
                  </TableCell>
                  <TableCell className="text-[12.5px] text-muted-foreground">
                    {createdLabel(tenant.createdAt, now)}
                  </TableCell>
                  <TableCell className="text-[12.5px] text-muted-foreground">
                    {defaultFor(tenant.id)}
                  </TableCell>
                  <TableCell className="text-right">
                    <OpenInAdmin
                      projectId={projectId}
                      environmentId={environmentId}
                      resource={resource}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </section>
  )
}
