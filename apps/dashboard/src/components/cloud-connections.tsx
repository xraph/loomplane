// The organization's cloud connections: a tile for each cloud Loomplane
// deploys to, with the accounts connected there. Credentials go in once and
// never come back; the API checks each with its cloud before keeping it.
import {
  CloudIcon,
  EllipsisIcon,
  PlusIcon,
  RefreshCwIcon,
  UploadIcon,
} from "lucide-react"
import { type FormEvent, useRef, useState } from "react"
import { toast } from "sonner"

import {
  CloudSQLRecoveryFields,
  cloudSQLRecoverySettings,
} from "@/components/cloudsql-recovery-fields"

import {
  RDSRecoveryFields,
  rdsRecoverySettings,
} from "@/components/rds-recovery-fields"

import { messageOf } from "@/client/errors"
import {
  useConnectionsCheck,
  useConnectionCloudsList,
  useConnectionInClusterCreate,
  useConnectionsCreate,
  useConnectionsDelete,
  useConnectionSignInsStart,
  useConnectionsList,
  useConnectionsReplace,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Setting,
  SignIn,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { KeylessSetup } from "@/components/keyless-setup"
import { MetricCard } from "@/components/metric-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  type Cloud,
  cloudLabel,
  clouds,
  isKeyJSON,
  rolesFor,
} from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { cloudCatalogue, cloudPurposes } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { rememberSignInCloud, rememberVercelState } from "@/lib/sign-in-cloud"
import { cn } from "@/lib/utils"

export function CloudConnections({ className }: { className?: string }) {
  const { can } = useOrganization()
  const mayManage = can("cloud_account:manage")
  const list = useQuery(useConnectionsList)
  const forms = useQuery(useConnectionCloudsList)
  const connections = list.data?.connections ?? []
  const tiles = cloudCatalogue(forms.data?.clouds, connections)

  return (
    <MetricCard icon={CloudIcon} title="Clouds" className={className}>
      <p className="-mt-1 max-w-3xl text-[13px] text-muted-foreground">
        Where Loomplane deploys. Each credential is checked with its cloud
        before it&apos;s kept, stored encrypted, and never shown again.
      </p>
      {list.status === "error" && (
        <Alert variant="destructive">
          <AlertDescription>
            {messageOf(list.error, "Cloud connections couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!list.data && list.status !== "error" ? (
        <div className="grid gap-3 md:grid-cols-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {tiles.map((t) => (
            <CloudTile
              key={t.cloud}
              cloud={t.cloud}
              title={t.title}
              connections={connections.filter((c) => c.cloud === t.cloud)}
              mayManage={mayManage}
            />
          ))}
        </div>
      )}
    </MetricCard>
  )
}

function CloudTile({
  cloud,
  title,
  connections,
  mayManage,
}: {
  cloud: string
  title: string
  connections: Connection[]
  mayManage: boolean
}) {
  const [connecting, setConnecting] = useState(false)
  return (
    <section
      aria-label={title}
      className="flex flex-col gap-4 rounded-xl bg-subtle p-4"
    >
      <div className="flex items-start gap-3">
        <CloudGlyph cloud={cloud} size="lg" />
        <div className="grid min-w-0 flex-1 gap-0.5">
          <h3 className="text-[14px] font-semibold">{title}</h3>
          <p className="text-xs text-muted-foreground">
            {cloudPurposes[cloud] ?? "Added by a provider plugin"}
          </p>
        </div>
        {mayManage && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setConnecting(true)}
          >
            <PlusIcon />
            {connections.length > 0 ? "Add" : "Connect"}
          </Button>
        )}
      </div>
      {connections.length === 0 ? (
        <p className="mt-auto border-t border-dashed pt-3 text-xs text-muted-foreground">
          {mayManage
            ? "Not connected."
            : "Not connected. An admin can connect it."}
        </p>
      ) : (
        <ul className="mt-auto grid gap-3 border-t pt-3">
          {connections.map((c) => (
            <ConnectionRow key={c.id} connection={c} mayManage={mayManage} />
          ))}
        </ul>
      )}
      <ConnectDialog
        open={connecting}
        onOpenChange={setConnecting}
        cloud={cloud}
      />
    </section>
  )
}

/**
 * needsSignInAgain is whether a signed-in connection can't be used as it is.
 * Signing in again is how it recovers, whether the sign-in lapsed or the
 * cloud refused it.
 */
export function needsSignInAgain(c: Connection): boolean {
  return (
    c.status === "sign_in_needed" ||
    (c.method === "oauth" && c.status !== "ready")
  )
}

/**
 * useSignInAgain starts a new sign-in for an existing connection. When it
 * finishes it replaces the connection's credential. The name stays as it
 * is, so none is sent.
 */
export function useSignInAgain() {
  const start = useMutation(useConnectionSignInsStart)
  const signInAgain = async (c: Connection) => {
    try {
      const started = await start.mutateAsync({
        body: { cloud: c.cloud, name: "", connectionId: c.id },
      })
      rememberSignInCloud(cloudLabel(c.cloud))
      if (c.cloud === "vercel") rememberVercelState((started as SignIn).state)
      // The generated hook doesn't type this response.
      window.location.assign((started as SignIn).authorizeUrl)
    } catch (err) {
      toast.error(messageOf(err, "Sign-in couldn't be started."))
    }
  }
  return { signInAgain, pending: start.isPending }
}

export const statusLabels: Record<string, string> = {
  ready: "Ready",
  incomplete: "Missing permissions",
  failed: "Refused",
  sign_in_needed: "Sign-in needed",
}

function ConnectionRow({
  connection: c,
  mayManage,
}: {
  connection: Connection
  mayManage: boolean
}) {
  const check = useMutation(useConnectionsCheck)
  const { signInAgain, pending: signingIn } = useSignInAgain()
  const [replacing, setReplacing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const roles = rolesFor(c.missing)
  const email = c.details.service_account as string | undefined

  const recheck = async () => {
    try {
      const next = await check.mutateAsync({ path: { id: c.id } })
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
    <li className="grid gap-2">
      <div className="flex items-center gap-3">
        <span
          aria-hidden="true"
          className={cn(
            "size-1.5 shrink-0 rounded-full",
            c.status === "ready" && "bg-overdye",
            c.status === "incomplete" && "bg-weld",
            c.status === "failed" && "bg-destructive"
          )}
        />
        <div className="grid min-w-0 flex-1 gap-0.5">
          <span className="truncate text-[13px] font-medium">{c.name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {statusLabels[c.status] ?? c.status}
            {c.account && (
              <>
                {" · "}
                <code className="font-mono">{c.account}</code>
              </>
            )}
            {c.checkedAt && (
              <>
                {" · checked "}
                <time dateTime={c.checkedAt}>{formatDate(c.checkedAt)}</time>
              </>
            )}
          </span>
        </div>
        {check.isPending && <Spinner className="text-muted-foreground" />}
        {mayManage && needsSignInAgain(c) && (
          <Button
            variant="outline"
            size="sm"
            disabled={signingIn}
            onClick={() => void signInAgain(c)}
          >
            {signingIn && <Spinner />}
            Sign in again
          </Button>
        )}
        {mayManage && (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="text-muted-foreground"
                  aria-label={`${c.name} options`}
                />
              }
            >
              <EllipsisIcon />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem
                disabled={check.isPending}
                onClick={() => void recheck()}
              >
                <RefreshCwIcon />
                Check again
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setReplacing(true)}>
                Replace credential
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setRemoving(true)}
              >
                Remove
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {c.status === "incomplete" && c.cloud === "aws" && (
        <Alert className="border-weld/50">
          <AlertTitle>
            It works, but can&apos;t do everything a deployment needs
          </AlertTitle>
          <AlertDescription className="grid gap-2">
            <p>Allow these actions for its IAM user, then check again:</p>
            <ul className="grid gap-1 font-mono text-xs">
              {c.missing.map((a) => (
                <li key={a}>{a}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      {c.status === "incomplete" && c.cloud !== "aws" && (
        <Alert className="border-weld/50">
          <AlertTitle>
            It works, but can&apos;t do everything a deployment needs
          </AlertTitle>
          <AlertDescription className="grid gap-2">
            <p>
              Grant {email ? <code className="font-mono">{email}</code> : "it"}{" "}
              {roles.length === 1 ? "this role" : "these roles"} on {c.account},
              then check again:
            </p>
            <ul className="grid gap-1">
              {roles.map((r) => (
                <li key={r.role}>
                  {r.title} <code className="font-mono text-xs">{r.role}</code>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      {c.status === "failed" && c.message && (
        <Alert variant="destructive">
          <AlertDescription>{c.message}</AlertDescription>
        </Alert>
      )}
      <ConnectDialog
        open={replacing}
        onOpenChange={setReplacing}
        replacing={c}
      />
      <RemoveDialog connection={c} open={removing} onOpenChange={setRemoving} />
    </li>
  )
}

export function RemoveDialog({
  connection: c,
  open,
  onOpenChange,
}: {
  connection: Connection
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const remove = useMutation(useConnectionsDelete)
  const invalidate = useInvalidate()

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: c.id } })
      invalidate(useConnectionsList)
      toast.success(`Removed ${c.name}`)
    } catch (err) {
      onOpenChange(false)
      toast.error(messageOf(err, "The connection wasn't removed."))
    }
  }

  // Controlled: Base UI's AlertDialogAction doesn't close the dialog.
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {c.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane deletes its credential and can&apos;t deploy to{" "}
            {c.account || cloudLabel(c.cloud)} until it&apos;s connected again.
            Nothing already running there is touched.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * ConnectDialog connects a cloud, starting on cloud, or with replacing set,
 * swaps an existing connection's credential for a new one.
 */
export function ConnectDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  cloud?: string
  replacing?: Connection
  /** keyless opens a replacing dialog on moving that connection to keyless. */
  keyless?: boolean
  onConnected?: (connection: Connection) => void
}) {
  return props.open ? (
    <ConnectForm
      key={`${props.cloud}:${props.replacing?.id ?? "new"}`}
      {...props}
    />
  ) : null
}

const linkClass =
  "text-sm text-muted-foreground underline underline-offset-4 hover:text-foreground"

/** SettingFields are the inputs for the settings a cloud's form lists. */
function SettingFields({
  fields,
  values,
  onChange,
}: {
  fields: Setting[]
  values: Record<string, string>
  onChange: (
    next: (current: Record<string, string>) => Record<string, string>
  ) => void
}) {
  return fields.map((st) => (
    <Field key={st.key}>
      <FieldLabel htmlFor={`connection-setting-${st.key}`}>
        {st.label}
        {/* Some labels already say so. */}
        {!st.required && !st.label.endsWith("(optional)") && " (optional)"}
      </FieldLabel>
      <Input
        id={`connection-setting-${st.key}`}
        value={values[st.key] ?? ""}
        onChange={(e) =>
          onChange((cur) => ({ ...cur, [st.key]: e.target.value }))
        }
        required={st.required}
      />
    </Field>
  ))
}

function ConnectForm({
  open,
  onOpenChange,
  cloud: initial = "gcp",
  replacing,
  keyless: startKeyless = false,
  onConnected,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  cloud?: string
  replacing?: Connection
  keyless?: boolean
  onConnected?: (connection: Connection) => void
}) {
  const create = useMutation(useConnectionsCreate)
  const createInCluster = useMutation(useConnectionInClusterCreate)
  const replace = useMutation(useConnectionsReplace)
  const signInStart = useMutation(useConnectionSignInsStart)
  const invalidate = useInvalidate()
  const file = useRef<HTMLInputElement>(null)
  const [cloud, setCloud] = useState<string>(replacing?.cloud ?? initial)
  // Clouds a provider plugin adds, with the form each one describes.
  const available = useQuery(useConnectionCloudsList)
  const extras = (available.data?.clouds ?? []).filter((c) => !c.builtin)
  const extra = extras.find((c) => c.cloud === cloud)
  // Signing in is offered first when the cloud supports it here, for a new
  // connection or in place of an existing one's credential. A person who
  // wants a token picks it for one cloud, and the choice resets if they
  // switch clouds.
  const entry = (available.data?.clouds ?? []).find((c) => c.cloud === cloud)
  const [tokenFor, setTokenFor] = useState<string | null>(null)
  // Keyless is the next choice after signing in, or the first where there
  // is no sign-in. Opening it shows the cloud's setup in place of the form.
  const [keylessFor, setKeylessFor] = useState<string | null>(
    startKeyless ? (replacing?.cloud ?? initial) : null
  )
  // Replacing a keyless connection's credential means giving it a key or
  // a sign-in; it's keyless already.
  const keylessOffered =
    !!entry?.methods.includes("keyless") &&
    (startKeyless || replacing?.method !== "keyless")
  const keylessOn = keylessOffered && keylessFor === cloud
  const signIn =
    !!entry?.methods.includes("oauth") && tokenFor !== cloud && !keylessOn
  const keylessFirst =
    keylessOffered && !entry?.methods.includes("oauth") && tokenFor !== cloud
  const tokenLink =
    cloud === "aws" ? "Use an access key instead" : "Use a token instead"
  // When Loomplane runs in a Kubernetes cluster it may deploy to, that
  // cluster comes first: it connects as Loomplane's own service account, so
  // there's no kubeconfig to paste. A replaced connection keeps the kind of
  // credential it has.
  const inClusterOffered =
    !replacing && !!entry?.methods.includes("in_cluster")
  const inClusterOn = inClusterOffered && tokenFor !== cloud
  const [extraSettings, setExtraSettings] = useState<Record<string, string>>(
    () => ({ ...(replacing?.details ?? {}) }) as Record<string, string>
  )
  // What the cloud asks for before sign-in, like Google's project. Until
  // someone types, a replaced connection shows the values it already has.
  const [signInValues, setSignInValues] = useState<Record<string, string>>({})
  const signInValue = (key: string) =>
    signInValues[key] ?? String(replacing?.details[key] ?? "")
  const [name, setName] = useState("")
  const [credential, setCredential] = useState("")
  const [team, setTeam] = useState(
    (replacing?.details.team_id as string | undefined) ?? ""
  )
  const [keyID, setKeyID] = useState("")
  const [region, setRegion] = useState(
    (replacing?.details.region as string | undefined) ?? "us-east-1"
  )
  const [certificate, setCertificate] = useState(
    (replacing?.details.certificate_arn as string | undefined) ?? ""
  )
  const [billingExport, setBillingExport] = useState(
    (replacing?.details.billing_export as string | undefined) ?? ""
  )
  const [error, setError] = useState("")
  const busy =
    create.isPending ||
    createInCluster.isPending ||
    replace.isPending ||
    signInStart.isPending

  const close = (next: boolean) => {
    if (!next) {
      setName("")
      setCredential("")
      setKeyID("")
      setError("")
    }
    onOpenChange(next)
  }

  const load = async (f: File | undefined) => {
    if (!f) return
    const text = await f.text()
    if (!isKeyJSON(text)) {
      setError("That file isn't a service account key.")
      return
    }
    setError("")
    setCredential(text)
  }

  const startSignIn = async () => {
    try {
      // Signing in for an existing connection replaces its credential and
      // keeps its name, so none is sent.
      const fields = entry?.signInSettings ?? []
      const settings =
        fields.length > 0
          ? {
              settings: Object.fromEntries(
                fields.map((f) => [f.key, signInValue(f.key).trim()])
              ),
            }
          : {}
      const started = await signInStart.mutateAsync({
        body: replacing
          ? { cloud, name: "", connectionId: replacing.id, ...settings }
          : { cloud, name: name.trim(), ...settings },
      })
      rememberSignInCloud(entry?.title ?? cloudLabel(cloud))
      if (cloud === "vercel") rememberVercelState((started as SignIn).state)
      // The generated hook doesn't type this response.
      window.location.assign((started as SignIn).authorizeUrl)
    } catch (err) {
      setError(messageOf(err, "Sign-in couldn't be started."))
    }
  }

  const connected = (c: Connection) => {
    invalidate(useConnectionsList)
    toast.success(
      c.status === "ready"
        ? `${c.name} is connected and ready`
        : `${c.name} is connected, but missing permissions`
    )
    onConnected?.(c)
    close(false)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    if (keylessOn) return
    if (signIn) {
      await startSignIn()
      return
    }
    const trimmed = Object.fromEntries(
      Object.entries(extraSettings)
        .map(([k, v]) => [k, v.trim()])
        .filter(([, v]) => v !== "")
    )
    if (inClusterOn) {
      try {
        connected(
          await createInCluster.mutateAsync({
            body: { name: name.trim(), settings: trimmed },
          })
        )
      } catch (err) {
        setError(messageOf(err, "The cluster wasn't connected."))
      }
      return
    }
    const gcpSettings = {
      ...cloudSQLRecoverySettings(extraSettings),
      ...(billingExport.trim() ? { billing_export: billingExport.trim() } : {}),
    }
    const settings = extra
      ? trimmed
      : cloud === "vercel" && team.trim()
        ? { team_id: team.trim() }
        : cloud === "aws"
          ? {
              region: region.trim(),
              ...rdsRecoverySettings(extraSettings),
              ...(certificate.trim()
                ? { certificate_arn: certificate.trim() }
                : {}),
            }
          : cloud === "gcp" && Object.keys(gcpSettings).length
            ? gcpSettings
            : undefined
    // An AWS key is two values; Loomplane stores them together.
    const secret =
      cloud === "aws"
        ? JSON.stringify({
            access_key_id: keyID.trim(),
            secret_access_key: credential.trim(),
          })
        : credential
    try {
      const c = replacing
        ? await replace.mutateAsync({
            path: { id: replacing.id },
            body: { credential: secret, settings },
          })
        : await create.mutateAsync({
            body: { cloud, name: name.trim(), credential: secret, settings },
          })
      connected(c)
    } catch (err) {
      setError(messageOf(err, "The cloud wasn't connected."))
    }
  }

  const info = (clouds as Record<string, (typeof clouds)[Cloud]>)[cloud]
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={(e) => void submit(e)} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>
              {replacing && keylessOn
                ? `Switch ${replacing.name} to keyless`
                : replacing
                  ? `Replace ${replacing.name}'s credential`
                  : "Connect an account"}
            </DialogTitle>
            <DialogDescription>
              {replacing && keylessOn
                ? "Loomplane checks the cloud's trust first. If the cloud refuses it, the connection stays as it is."
                : replacing
                  ? "The new credential is checked first. If the cloud refuses it, the old one stays."
                  : "Loomplane checks the credential with the cloud before keeping it."}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            {!replacing && (
              <>
                <Field>
                  <FieldLabel>Cloud</FieldLabel>
                  <ToggleGroup
                    variant="outline"
                    spacing={1}
                    className="flex-wrap"
                    value={[cloud]}
                    onValueChange={(v) => {
                      if (v[0]) setCloud(v[0])
                    }}
                  >
                    <ToggleGroupItem value="gcp">Google Cloud</ToggleGroupItem>
                    <ToggleGroupItem value="vercel">Vercel</ToggleGroupItem>
                    <ToggleGroupItem value="aws">AWS</ToggleGroupItem>
                    {extras.map((c) => (
                      <ToggleGroupItem key={c.cloud} value={c.cloud}>
                        {c.title}
                      </ToggleGroupItem>
                    ))}
                  </ToggleGroup>
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-name">Name</FieldLabel>
                  <Input
                    id="connection-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={
                      cloud === "gcp"
                        ? "Production project"
                        : cloud === "aws"
                          ? "Production account"
                          : "Web team"
                    }
                    required
                  />
                </Field>
              </>
            )}
            {signIn &&
              (entry?.signInSettings ?? []).map((f) => (
                <Field key={f.key}>
                  <FieldLabel htmlFor={`sign-in-${f.key}`}>
                    {f.label}
                  </FieldLabel>
                  {/* The server keeps a replaced connection's own
                      placement, so it can't be changed here. */}
                  <Input
                    id={`sign-in-${f.key}`}
                    value={signInValue(f.key)}
                    onChange={(e) =>
                      setSignInValues({
                        ...signInValues,
                        [f.key]: e.target.value,
                      })
                    }
                    readOnly={!!replacing}
                    autoComplete="off"
                    required={f.required}
                  />
                </Field>
              ))}
            {signIn &&
              replacing &&
              (entry?.signInSettings ?? []).length > 0 && (
                <FieldDescription>
                  A new sign-in keeps this connection where it is. To deploy
                  somewhere else, make a new connection.
                </FieldDescription>
              )}
            {keylessOn ? (
              <div className="grid justify-items-start gap-3">
                <KeylessSetup
                  cloud={cloud}
                  title={entry?.title ?? cloudLabel(cloud)}
                  name={name}
                  fields={entry?.keylessSettings ?? []}
                  replacing={replacing}
                  onConnected={(c) => {
                    onConnected?.(c)
                    close(false)
                  }}
                />
                {!startKeyless && (
                  <button
                    type="button"
                    className={linkClass}
                    onClick={() => setKeylessFor(null)}
                  >
                    Back
                  </button>
                )}
              </div>
            ) : keylessFirst ? (
              <div className="grid justify-items-start gap-2">
                <Button type="button" onClick={() => setKeylessFor(cloud)}>
                  Connect keylessly
                </Button>
                <button
                  type="button"
                  className={linkClass}
                  onClick={() => setTokenFor(cloud)}
                >
                  {tokenLink}
                </button>
              </div>
            ) : signIn ? (
              <div className="grid justify-items-start gap-2">
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner />}
                  Sign in with {entry?.title ?? cloud}
                </Button>
                {keylessOffered && (
                  <button
                    type="button"
                    className={linkClass}
                    onClick={() => setKeylessFor(cloud)}
                  >
                    Connect keylessly instead
                  </button>
                )}
                <button
                  type="button"
                  className={linkClass}
                  onClick={() => setTokenFor(cloud)}
                >
                  {tokenLink}
                </button>
              </div>
            ) : inClusterOn ? (
              <>
                <FieldDescription>
                  Loomplane runs in a Kubernetes cluster that it may deploy to.
                  It connects as its own service account, so there&apos;s no
                  kubeconfig to paste. Choose a namespace your operator gave it,
                  never the one Loomplane runs in.
                </FieldDescription>
                <SettingFields
                  fields={entry?.inClusterSettings ?? []}
                  values={extraSettings}
                  onChange={setExtraSettings}
                />
                <button
                  type="button"
                  className={`${linkClass} justify-self-start`}
                  onClick={() => setTokenFor(cloud)}
                >
                  Use a kubeconfig instead
                </button>
              </>
            ) : cloud === "gcp" ? (
              <>
                <Field>
                  <FieldLabel htmlFor="connection-key">
                    {info.credential}
                  </FieldLabel>
                  <Textarea
                    id="connection-key"
                    value={credential}
                    onChange={(e) => setCredential(e.target.value)}
                    placeholder='{"type": "service_account", ...}'
                    spellCheck={false}
                    className="max-h-40 min-h-24 font-mono text-xs md:text-xs"
                    required
                  />
                  <div className="flex items-center gap-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"

                      onClick={() => file.current?.click()}
                    >
                      <UploadIcon />
                      Choose the key file
                    </Button>
                    <input
                      ref={file}
                      type="file"
                      accept="application/json,.json"
                      className="hidden"
                      onChange={(e) => void load(e.target.files?.[0])}
                    />
                  </div>
                  <FieldDescription>
                    A JSON key for a service account in the project you deploy
                    to. It needs Cloud Run Admin, Cloud Build Editor, Artifact
                    Registry Administrator, Storage Admin, Secret Manager Admin,
                    Cloud SQL Admin, Cloud Memorystore Redis Admin, Service
                    Account User and Browser; Loomplane says which are missing.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-billing-export">
                    Billing export table (optional)
                  </FieldLabel>
                  <Input
                    id="connection-billing-export"
                    autoComplete="off"
                    value={billingExport}
                    onChange={(e) => setBillingExport(e.target.value)}
                    placeholder="project.dataset.gcp_billing_export_v1_..."
                    spellCheck={false}
                    className="font-mono"
                  />
                  <FieldDescription>
                    Google Cloud reports cost only through a billing export to
                    BigQuery. Name its table and Loomplane reads this
                    project&apos;s spend from it, with BigQuery Job User on the
                    project and BigQuery Data Viewer on the dataset.
                  </FieldDescription>
                </Field>
                <CloudSQLRecoveryFields
                  value={extraSettings}
                  onChange={setExtraSettings}
                />
              </>
            ) : cloud === "aws" ? (
              <>
                <Field>
                  <FieldLabel htmlFor="connection-key-id">
                    Access key ID
                  </FieldLabel>
                  <Input
                    id="connection-key-id"
                    autoComplete="off"
                    value={keyID}
                    onChange={(e) => setKeyID(e.target.value)}
                    placeholder="AKIA..."
                    spellCheck={false}
                    className="font-mono"
                    required
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-secret">
                    Secret access key
                  </FieldLabel>
                  <Input
                    id="connection-secret"
                    type="password"
                    autoComplete="off"
                    value={credential}
                    onChange={(e) => setCredential(e.target.value)}
                    required
                  />
                  <FieldDescription>
                    An access key for an IAM user Loomplane deploys as. It needs
                    ECS, ECR, CodeBuild, load balancing, IAM roles, RDS and
                    Secrets Manager; Loomplane lists any action it lacks.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-region">Region</FieldLabel>
                  <Input
                    id="connection-region"
                    value={region}
                    onChange={(e) => setRegion(e.target.value)}
                    placeholder="us-east-1"
                    spellCheck={false}
                    required
                  />
                  <FieldDescription>
                    Everything Loomplane creates in this account goes here.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-certificate">
                    Certificate ARN (optional)
                  </FieldLabel>
                  <Input
                    id="connection-certificate"
                    value={certificate}
                    onChange={(e) => setCertificate(e.target.value)}
                    placeholder={`arn:aws:acm:${region || "us-east-1"}:...:certificate/...`}
                    spellCheck={false}
                    className="font-mono text-xs md:text-xs"
                  />
                  <FieldDescription>
                    An ACM certificate in the same region. With one, public
                    services on ECS answer over HTTPS; without, the load
                    balancer serves plain HTTP and plans warn you.
                  </FieldDescription>
                </Field>
                <RDSRecoveryFields
                  value={extraSettings}
                  onChange={setExtraSettings}
                />
              </>
            ) : extra ? (
              <>
                <Field>
                  <FieldLabel htmlFor="connection-credential">
                    {extra.credential || "Credential"}
                  </FieldLabel>
                  <Textarea
                    id="connection-credential"
                    value={credential}
                    onChange={(e) => setCredential(e.target.value)}
                    spellCheck={false}
                    autoComplete="off"
                    className="max-h-40 min-h-16 font-mono text-xs md:text-xs"
                    required
                  />
                  {extra.help && (
                    <FieldDescription>{extra.help}</FieldDescription>
                  )}
                </Field>
                <SettingFields
                  fields={extra.settings ?? []}
                  values={extraSettings}
                  onChange={setExtraSettings}
                />
                {inClusterOffered && (
                  <button
                    type="button"
                    className={`${linkClass} justify-self-start`}
                    onClick={() => setTokenFor(null)}
                  >
                    Connect the cluster Loomplane runs in instead
                  </button>
                )}
              </>
            ) : (
              <>
                <Field>
                  <FieldLabel htmlFor="connection-token">
                    {info.credential}
                  </FieldLabel>
                  <Input
                    id="connection-token"
                    type="password"
                    autoComplete="off"
                    value={credential}
                    onChange={(e) => setCredential(e.target.value)}
                    required
                  />
                  <FieldDescription>
                    Create one under Account Settings, then Tokens, scoped to
                    the team you deploy to.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="connection-team">
                    Team ID (optional)
                  </FieldLabel>
                  <Input
                    id="connection-team"
                    value={team}
                    onChange={(e) => setTeam(e.target.value)}
                    placeholder="team_..."
                  />
                  <FieldDescription>
                    Leave it empty to deploy to your personal account.
                  </FieldDescription>
                </Field>
              </>
            )}
          </FieldGroup>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"

              onClick={() => close(false)}
            >
              Cancel
            </Button>
            {!signIn && !keylessOn && !keylessFirst && (
              <Button type="submit" disabled={busy}>
                {busy && <Spinner />}
                {replacing ? "Check and replace" : "Check and connect"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
