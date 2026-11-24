// One workspace service: where it is, what its last check found, which
// projects use it, who may, and the credentials Loomplane keeps for it.
import { DatabaseIcon } from "lucide-react"
import { type FormEvent, useId, useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useCatalogEndpointsClearAdmin,
  useCatalogEndpointsDelete,
  useCatalogEndpointsGet,
  useCatalogEndpointsList,
  useCatalogEndpointsRecheck,
  useCatalogEndpointsReplaceCredential,
  useCatalogEndpointsSetAdmin,
  useCatalogEndpointsShare,
  useCatalogEndpointsUpdate,
  useCatalogEndpointsUses,
  useConnectionsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { CatalogconnectEndpoint } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  CloudCallout,
  CloudConfirm,
  CloudPageHeader,
  FactList,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { Page } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  capabilityWords,
  CheckFrom,
  CheckReport,
  useCheckTargets,
} from "@/components/workspace-services/check-parts"
import {
  engineLogos,
  ProjectPicker,
} from "@/components/workspace-services/register-dialog"
import {
  connectionHints,
  engineTitles,
  type ConnectionEngine,
  maskConnection,
  parseConnection,
} from "@/lib/connection-string"
import { formatDate, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

const title = (engine: string) =>
  engineTitles[engine as ConnectionEngine] ?? engine

export function WorkspaceService({ id }: { id: string }) {
  const endpoint = useQuery(useCatalogEndpointsGet, { path: { id } })
  const name = endpoint.data?.name ?? "Service"
  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Catalog", to: "/services" }, { label: name }]}
      />
      <Page>
        {endpoint.status === "error" ? (
          statusOf(endpoint.error) === 404 ? (
            <CloudEmpty
              kind="notice"
              icon={DatabaseIcon}
              title="No such workspace service"
              action={
                <Button nativeButton={false} render={<Link to="/services" />}>
                  Back to the catalog
                </Button>
              }
            >
              It may have been deleted, or it belongs to another workspace.
            </CloudEmpty>
          ) : (
            <QueryError
              error={endpoint.error}
              fallback="It couldn't be loaded."
              retry={endpoint.refetch}
            />
          )
        ) : !endpoint.data ? (
          <div className="grid gap-5" aria-busy="true">
            <Skeleton className="h-11 w-72" />
            <Skeleton className="h-72 rounded-xl" />
          </div>
        ) : (
          <Loaded endpoint={endpoint.data} />
        )}
      </Page>
    </>
  )
}

function Loaded({ endpoint: e }: { endpoint: CatalogconnectEndpoint }) {
  const { can } = useOrganization()
  const mayManage =
    can("secret:manage") && (e.origin === "" || e.origin === "registered")
  const uses = useQuery(useCatalogEndpointsUses, { path: { id: e.id } })
  const useList = uses.data?.uses ?? []
  const [tab, setTab] = useState("overview")
  const [deleting, setDeleting] = useState(false)
  const check = e.lastCheck
  const state = !check
    ? { tone: "quiet" as const, label: "Not checked" }
    : check.ok
      ? { tone: "live" as const, label: "Checks out" }
      : { tone: "failed" as const, label: "Check failed" }

  return (
    <>
      <CloudPageHeader
        glyph={
          <ProductIcon
            product={engineLogos[e.engine]}
            fallback={DatabaseIcon}
            label={title(e.engine)}
            size="lg"
          />
        }
        title={e.name}
        mono
        status={<StatusPill tone={state.tone}>{state.label}</StatusPill>}
        meta={
          <>
            {title(e.engine)} ·{" "}
            <code className="font-mono text-[12.5px]">
              {e.host}:{e.port}
              {e.database ? `/${e.database}` : ""}
            </code>
            {e.environmentLabel && ` · ${e.environmentLabel}`}
            {e.description && ` · ${e.description}`}
          </>
        }
        actions={
          mayManage && (
            <PageMenu label="More service actions">
              <DropdownMenuItem onClick={() => setTab("settings")}>
                Replace credentials
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setDeleting(true)}
              >
                Delete
              </DropdownMenuItem>
            </PageMenu>
          )
        }
      />
      {!(e.origin === "" || e.origin === "registered") && (
        <CloudCallout tone="info" title="This belongs to a provider resource">
          Loomplane made it when a cloud resource was connected. Change it from
          that resource, on the Catalog page.
        </CloudCallout>
      )}

      <Tabs value={tab} onValueChange={(v) => setTab(v as string)}>
        <TabsList
          variant="line"
          aria-label="Service views"
          className="mb-5 max-w-full overflow-x-auto"
        >
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="uses">
            Used by
            {uses.data && <TabsCount>{useList.length}</TabsCount>}
          </TabsTrigger>
          <TabsTrigger value="access">Access</TabsTrigger>
          {mayManage && <TabsTrigger value="settings">Settings</TabsTrigger>}
        </TabsList>

        <TabsContent value="overview" className="grid gap-6">
          <Overview endpoint={e} mayCheck={mayManage} />
        </TabsContent>
        <TabsContent value="uses" className="grid gap-6">
          <Uses endpoint={e} />
        </TabsContent>
        <TabsContent value="access" className="grid max-w-[760px] gap-6">
          <Sharing endpoint={e} mayManage={mayManage} />
          <AdminCredential endpoint={e} mayManage={mayManage} />
        </TabsContent>
        {mayManage && (
          <TabsContent value="settings" className="grid max-w-[760px] gap-6">
            <Details endpoint={e} />
            <ReplaceCredentials endpoint={e} />
            <DangerZone
              endpoint={e}
              uses={useList.length}
              onDelete={() => setDeleting(true)}
            />
          </TabsContent>
        )}
      </Tabs>
      {mayManage && (
        <DeleteService
          endpoint={e}
          open={deleting}
          onOpenChange={setDeleting}
          uses={useList.length}
        />
      )}
    </>
  )
}

function Overview({
  endpoint: e,
  mayCheck,
}: {
  endpoint: CatalogconnectEndpoint
  mayCheck: boolean
}) {
  const now = useNow(60_000)
  const connections = useQuery(useConnectionsList)
  const from = connections.data?.connections.find(
    (c) => c.id === e.lastCheck?.target
  )
  const tls =
    e.tls === "verify-full"
      ? "Verified certificate"
      : e.tls === "require"
        ? "Encrypted, certificate not verified"
        : "Off"
  return (
    <>
      <section aria-label="Connection" className="grid gap-2.5">
        <CloudSectionHead
          title="Connection"
          description="Where it is. The password is in Vault, and never shown."
        />
        <FactList
          facts={[
            { label: "Engine", value: title(e.engine) },
            {
              label: "Host",
              value: (
                <code className="font-mono text-[12.5px]">
                  {e.host}:{e.port}
                </code>
              ),
            },
            {
              label: "Database",
              value: e.database ? (
                <code className="font-mono text-[12.5px]">{e.database}</code>
              ) : (
                <span className="text-muted-foreground">Its default</span>
              ),
            },
            {
              label: "User",
              value: e.username ? (
                <code className="font-mono text-[12.5px]">{e.username}</code>
              ) : (
                <span className="text-muted-foreground">None</span>
              ),
            },
            { label: "TLS", value: tls },
            {
              label: "Credentials",
              value:
                e.revision > 1
                  ? `Replaced ${e.revision - 1} ${e.revision === 2 ? "time" : "times"}`
                  : "As registered",
            },
            {
              label: "Registered",
              value: (
                <time dateTime={e.createdAt}>{formatDate(e.createdAt)}</time>
              ),
            },
          ]}
        />
      </section>
      <section aria-label="Last check" className="grid gap-2.5">
        <CloudSectionHead
          title="Last check"
          description={
            e.lastCheck
              ? `${relative(Date.parse(e.lastCheck.at), now)}${from ? `, from ${from.name}` : ""}.`
              : "It hasn't been checked since it was registered."
          }
        />
        {e.lastCheck && (
          <CheckReport engine={e.engine} result={e.lastCheck} tls={e.tls} />
        )}
        {e.lastCheck?.admin && (
          <p className="text-[12.5px] text-muted-foreground">
            {capabilityWords(e.lastCheck.admin, "The admin credential").join(
              " "
            )}
          </p>
        )}
        {mayCheck && (
          <Recheck endpoint={e} initial={e.lastCheck?.target ?? ""} />
        )}
      </section>
    </>
  )
}

function Recheck({
  endpoint: e,
  initial,
}: {
  endpoint: CatalogconnectEndpoint
  initial: string
}) {
  const ids = useId()
  const recheck = useMutation(useCatalogEndpointsRecheck)
  const { targets } = useCheckTargets()
  const [target, setTarget] = useState(initial)
  const from = target || targets[0]?.connection.id || ""
  const run = async () => {
    try {
      const out = (await recheck.mutateAsync({
        path: { id: e.id },
        body: { target: from },
      })) as CatalogconnectEndpoint
      if (out.lastCheck?.ok) toast.success(`${e.name} checks out`)
      else toast.error(`${e.name} failed its check`)
    } catch (cause) {
      toast.error(messageOf(cause, "The check didn't run."))
    }
  }
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field className="w-auto">
        <FieldLabel htmlFor={`${ids}-from`}>Check from</FieldLabel>
        <CheckFrom id={`${ids}-from`} value={from} onChange={setTarget} />
      </Field>
      <Button
        variant="outline"
        disabled={!from || recheck.isPending}
        onClick={() => void run()}
      >
        {recheck.isPending && <Spinner data-icon="inline-start" />}
        Check again
      </Button>
    </div>
  )
}

function Uses({ endpoint: e }: { endpoint: CatalogconnectEndpoint }) {
  const uses = useQuery(useCatalogEndpointsUses, { path: { id: e.id } })
  if (uses.status === "error")
    return (
      <QueryError
        error={uses.error}
        fallback="What uses it couldn't be loaded."
        retry={uses.refetch}
      />
    )
  if (!uses.data) return <Skeleton className="h-24 rounded-xl" />
  const list = uses.data.uses
  if (list.length === 0)
    return (
      <CloudEmpty kind="list" icon={DatabaseIcon} title="Nothing uses it yet">
        In a project, choose Add service, then Workspace services, to connect
        one of its services to {e.name}.
      </CloudEmpty>
    )
  const byProject = new Map<string, typeof list>()
  for (const u of list)
    byProject.set(u.projectId, [...(byProject.get(u.projectId) ?? []), u])
  return (
    <section aria-label="Used by" className="grid gap-2.5">
      <CloudSectionHead
        title="Used by"
        count={list.length}
        description="Each project environment that connects to it, as it last deployed."
      />
      <ul className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
        {[...byProject].map(([projectId, rows]) => (
          <li key={projectId} className="grid gap-1 px-4 py-3">
            <Link
              to="/projects/$id"
              params={{ id: projectId }}
              className="text-[13.5px] font-semibold text-foreground hover:underline hover:underline-offset-4"
            >
              {rows[0].project || projectId}
            </Link>
            <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[12.5px] text-muted-foreground">
              {rows.map((u) => (
                <li key={u.environmentId + u.resource}>
                  <span className="font-mono text-foreground">
                    {u.resource}
                  </span>{" "}
                  in {u.environment || u.environmentId}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </section>
  )
}

function Sharing({
  endpoint: e,
  mayManage,
}: {
  endpoint: CatalogconnectEndpoint
  mayManage: boolean
}) {
  const share = useMutation(useCatalogEndpointsShare)
  const invalidate = useInvalidate()
  const projects = useQuery(useProjectsList)
  const [restricted, setRestricted] = useState(e.restricted)
  const [picked, setPicked] = useState<string[]>(e.projects ?? [])
  const list = (projects.data?.projects ?? []).filter(
    (p) => p.kind !== "service"
  )
  const names = new Map(list.map((p) => [p.id, p.name]))
  const changed =
    restricted !== e.restricted ||
    (restricted &&
      [...picked].sort().join() !== [...(e.projects ?? [])].sort().join())
  const save = async () => {
    try {
      await share.mutateAsync({
        path: { id: e.id },
        body: { restricted, projects: restricted ? picked : [] },
      })
      invalidate(useCatalogEndpointsList)
      toast.success(
        restricted
          ? `${e.name} is shared with ${picked.length === 1 ? "one project" : `${picked.length} projects`}`
          : `Every project can use ${e.name}`
      )
    } catch (cause) {
      toast.error(messageOf(cause, "Sharing wasn't saved."))
    }
  }
  return (
    <section aria-label="Sharing" className="grid gap-2.5">
      <CloudSectionHead
        title="Who can use it"
        description="A project that loses access keeps what it deployed until its next plan, which refuses to use it."
      />
      {!mayManage ? (
        <p className="text-[13px]">
          {e.restricted
            ? `Only ${e.projects.map((id) => names.get(id) ?? id).join(", ") || "no project"}.`
            : "Every project in the workspace."}
        </p>
      ) : (
        <>
          <div
            role="radiogroup"
            aria-label="Who can use it"
            className="grid gap-2 sm:grid-cols-2"
          >
            {[
              { value: false, label: "Every project" },
              { value: true, label: "Only some projects" },
            ].map((o) => (
              <button
                key={o.label}
                type="button"
                role="radio"
                aria-checked={restricted === o.value}
                onClick={() => setRestricted(o.value)}
                className={cn(
                  "rounded-lg px-3 py-2.5 text-left text-[13px] font-medium ring-1",
                  restricted === o.value
                    ? "bg-primary/5 ring-primary"
                    : "ring-border hover:bg-muted/50"
                )}
              >
                {o.label}
              </button>
            ))}
          </div>
          {restricted && (
            <ProjectPicker
              projects={list}
              picked={picked}
              onChange={setPicked}
            />
          )}
          <div>
            <Button
              disabled={
                !changed ||
                share.isPending ||
                (restricted && picked.length === 0)
              }
              onClick={() => void save()}
            >
              {share.isPending && <Spinner data-icon="inline-start" />}
              Save sharing
            </Button>
          </div>
        </>
      )}
    </section>
  )
}

/**
 * CredentialForm takes a connection string, checks it from a target, and
 * hands both to submit. The server checks it again before keeping it.
 */
function CredentialForm({
  label,
  description,
  submitLabel,
  placeholder,
  pending,
  onSubmit,
}: {
  label: string
  description: string
  submitLabel: string
  placeholder: string
  pending: boolean
  onSubmit: (dsn: string, target: string) => Promise<boolean>
}) {
  const ids = useId()
  const { targets } = useCheckTargets()
  const [dsn, setDsn] = useState("")
  const [target, setTarget] = useState("")
  const from = target || targets[0]?.connection.id || ""
  const hints = connectionHints(dsn)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (await onSubmit(dsn.trim(), from)) setDsn("")
  }
  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-3">
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor={`${ids}-dsn`}>{label}</FieldLabel>
          <Input
            id={`${ids}-dsn`}
            type="password"
            className="font-mono"
            value={dsn}
            autoComplete="new-password"
            spellCheck={false}
            placeholder={placeholder}
            onChange={(ev) => setDsn(ev.target.value)}
          />
          <FieldDescription>
            {dsn && parseConnection(dsn)
              ? `Reads as ${maskConnection(dsn)}. `
              : ""}
            {description}
          </FieldDescription>
          {hints.map((h) => (
            <p key={h.message} className="text-[12.5px] text-weld-ink">
              {h.message}
            </p>
          ))}
        </Field>
        <Field className="w-auto">
          <FieldLabel htmlFor={`${ids}-from`}>Check from</FieldLabel>
          <CheckFrom id={`${ids}-from`} value={from} onChange={setTarget} />
        </Field>
      </FieldGroup>
      <div>
        <Button type="submit" disabled={!dsn.trim() || !from || pending}>
          {pending && <Spinner data-icon="inline-start" />}
          {submitLabel}
        </Button>
      </div>
    </form>
  )
}

function AdminCredential({
  endpoint: e,
  mayManage,
}: {
  endpoint: CatalogconnectEndpoint
  mayManage: boolean
}) {
  const set = useMutation(useCatalogEndpointsSetAdmin)
  const clear = useMutation(useCatalogEndpointsClearAdmin)
  const [error, setError] = useState("")
  const admin = e.lastCheck?.admin
  return (
    <section aria-label="Admin credential" className="grid gap-2.5">
      <CloudSectionHead
        title="Admin credential"
        description="Optional. With one, Loomplane can give each service its own database or user on this server, instead of sharing one."
      >
        {e.adminSet && <StatusPill tone="live">Kept</StatusPill>}
      </CloudSectionHead>
      {e.adminSet && admin && (
        <p className="text-[13px] text-muted-foreground">
          {capabilityWords(admin, "It").join(" ")}
        </p>
      )}
      {mayManage && (
        <>
          <CredentialForm
            label={
              e.adminSet
                ? "Replace the admin connection string"
                : "Admin connection string"
            }
            description={`It must reach ${e.host}:${e.port}, like the service's own. Loomplane checks it, then keeps it in Vault.`}
            submitLabel={e.adminSet ? "Check and replace" : "Check and keep"}
            placeholder={`${e.engine === "mongodb" ? "mongodb" : e.engine}://admin:password@${e.host}:${e.port}/`}
            pending={set.isPending}
            onSubmit={async (dsn, target) => {
              setError("")
              try {
                await set.mutateAsync({
                  path: { id: e.id },
                  body: { dsn, target },
                })
                toast.success("The admin credential is kept")
                return true
              } catch (cause) {
                setError(messageOf(cause, "The admin credential wasn't kept."))
                return false
              }
            }}
          />
          {e.adminSet && (
            <div>
              <Button
                variant="outline"
                className="text-destructive"
                disabled={clear.isPending}
                onClick={() =>
                  void clear
                    .mutateAsync({ path: { id: e.id } })
                    .then(() =>
                      toast.success("The admin credential is forgotten")
                    )
                    .catch((cause: unknown) =>
                      setError(messageOf(cause, "It wasn't forgotten."))
                    )
                }
              >
                Forget the admin credential
              </Button>
            </div>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </>
      )}
    </section>
  )
}

function Details({ endpoint: e }: { endpoint: CatalogconnectEndpoint }) {
  const ids = useId()
  const update = useMutation(useCatalogEndpointsUpdate)
  const [name, setName] = useState(e.name)
  const [description, setDescription] = useState(e.description)
  const [label, setLabel] = useState(e.environmentLabel)
  const changed =
    name !== e.name ||
    description !== e.description ||
    label !== e.environmentLabel
  const save = async (event: FormEvent) => {
    event.preventDefault()
    try {
      await update.mutateAsync({
        path: { id: e.id },
        body: {
          name: name.trim(),
          description: description.trim(),
          environmentLabel: label.trim(),
        },
      })
      toast.success("Saved")
    } catch (cause) {
      toast.error(messageOf(cause, "It wasn't saved."))
    }
  }
  return (
    <section aria-label="Details" className="grid gap-2.5">
      <CloudSectionHead
        title="Details"
        description="Projects pick it by its name."
      />
      <form onSubmit={(ev) => void save(ev)} className="grid gap-3">
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor={`${ids}-name`}>Name</FieldLabel>
            <Input
              id={`${ids}-name`}
              value={name}
              maxLength={100}
              required
              onChange={(ev) => setName(ev.target.value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${ids}-description`}>Description</FieldLabel>
            <Input
              id={`${ids}-description`}
              value={description}
              maxLength={200}
              onChange={(ev) => setDescription(ev.target.value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${ids}-label`}>
              Environment it serves
            </FieldLabel>
            <Input
              id={`${ids}-label`}
              value={label}
              maxLength={40}
              className="w-48"
              onChange={(ev) => setLabel(ev.target.value)}
            />
          </Field>
        </FieldGroup>
        <div>
          <Button
            type="submit"
            disabled={!changed || !name.trim() || update.isPending}
          >
            {update.isPending && <Spinner data-icon="inline-start" />}
            Save details
          </Button>
        </div>
      </form>
    </section>
  )
}

function ReplaceCredentials({
  endpoint: e,
}: {
  endpoint: CatalogconnectEndpoint
}) {
  const replace = useMutation(useCatalogEndpointsReplaceCredential)
  const [error, setError] = useState("")
  return (
    <section aria-label="Replace credentials" className="grid gap-2.5">
      <CloudSectionHead
        title="Replace credentials"
        description="After a password rotation or a move to a new primary. Services get the new ones the next time they deploy."
      />
      <CredentialForm
        label="New connection string"
        description={`The same engine, ${title(e.engine)}. Loomplane checks it before it replaces the old one.`}
        submitLabel="Check and replace"
        placeholder={`${e.engine}://user:new-password@${e.host}:${e.port}/${e.database ?? ""}`}
        pending={replace.isPending}
        onSubmit={async (dsn, target) => {
          setError("")
          try {
            await replace.mutateAsync({
              path: { id: e.id },
              body: { dsn, target, revision: e.revision },
            })
            toast.success(`${e.name}'s credentials are replaced`, {
              description: "Each service picks them up on its next deploy.",
            })
            return true
          } catch (cause) {
            setError(
              statusOf(cause) === 409
                ? "Someone changed it since this page loaded. Reload, then try again."
                : messageOf(cause, "The credentials weren't replaced.")
            )
            return false
          }
        }}
      />
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </section>
  )
}

function DangerZone({
  endpoint: e,
  uses,
  onDelete,
}: {
  endpoint: CatalogconnectEndpoint
  uses: number
  onDelete: () => void
}) {
  return (
    <section aria-label="Danger zone" className="grid gap-2.5">
      <CloudSectionHead title="Danger zone" />
      <div className="flex flex-wrap items-center gap-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/25">
        <div className="grid min-w-0 flex-[1_1_360px] gap-1">
          <p className="text-[13px] font-semibold">Delete {e.name}</p>
          <p className="text-[12.5px] leading-normal text-muted-foreground">
            {uses > 0
              ? `${uses === 1 ? "One project environment uses" : `${uses} project environments use`} it, so it can't be deleted until they stop.`
              : "Loomplane forgets it and its credentials. Nothing on the server changes."}
          </p>
        </div>
        <Button
          variant="outline"
          disabled={uses > 0}
          className="border-destructive/25 text-destructive hover:bg-destructive/10 hover:text-destructive"
          onClick={onDelete}
        >
          Delete
        </Button>
      </div>
    </section>
  )
}

function DeleteService({
  endpoint: e,
  open,
  onOpenChange,
  uses,
}: {
  endpoint: CatalogconnectEndpoint
  open: boolean
  onOpenChange: (open: boolean) => void
  uses: number
}) {
  const remove = useMutation(useCatalogEndpointsDelete)
  const invalidate = useInvalidate()
  const navigate = useNavigate()
  const [error, setError] = useState("")
  const confirm = async () => {
    setError("")
    try {
      await remove.mutateAsync({ path: { id: e.id } })
      invalidate(useCatalogEndpointsList)
      toast.success(`Deleted ${e.name}`)
      onOpenChange(false)
      void navigate({ to: "/services", replace: true })
    } catch (cause) {
      setError(messageOf(cause, "It wasn't deleted."))
    }
  }
  return (
    <CloudConfirm
      open={open}
      onOpenChange={(next) => {
        if (!next) setError("")
        onOpenChange(next)
      }}
      title={`Delete ${e.name}?`}
      confirmLabel="Delete"
      typeToConfirm={e.name}
      pending={remove.isPending}
      onConfirm={() => void confirm()}
      error={error && <CloudCallout tone="error" title={error} />}
    >
      {uses > 0
        ? "Projects still use it; the deletion will be refused until they stop."
        : "Loomplane forgets it and its credentials. Nothing on the server changes."}
    </CloudConfirm>
  )
}
