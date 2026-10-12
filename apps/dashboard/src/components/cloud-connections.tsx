// The organization's cloud connections: the Google Cloud projects and
// Vercel accounts it deploys to. Credentials go in once and never come
// back; the API checks each with its cloud before keeping it.
import { CloudIcon, RefreshCwIcon, Trash2Icon, UploadIcon } from "lucide-react"
import { type FormEvent, useRef, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionsCheck,
  useConnectionsCreate,
  useConnectionsDelete,
  useConnectionsList,
  useConnectionsReplace,
} from "@/client/forge/loomplane/hooks"
import type { Connection } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
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
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

export function CloudConnections() {
  const { can } = useOrganization()
  const mayManage = can("cloud_account:manage")
  const list = useQuery(useConnectionsList)
  const [adding, setAdding] = useState(false)
  const connections = list.data?.connections ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>Clouds</CardTitle>
        <CardDescription>
          The Google Cloud projects and Vercel accounts Loomplane deploys to.
          Each credential is checked with its cloud before it&apos;s kept,
          stored encrypted, and never shown again.
        </CardDescription>
        {mayManage && (
          <CardAction>
            <Button className="rounded-full" onClick={() => setAdding(true)}>
              <CloudIcon />
              Connect a cloud
            </Button>
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="grid gap-3">
        {list.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(list.error, "Cloud connections couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!list.data && list.status !== "error" && (
          <Skeleton className="h-24 w-full" />
        )}
        {list.data && connections.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {mayManage
              ? "No clouds connected yet. Connect Google Cloud for services that run as containers, and Vercel for web apps."
              : "No clouds connected yet. An admin can connect one."}
          </p>
        )}
        {connections.map((c) => (
          <ConnectionRow key={c.id} connection={c} mayManage={mayManage} />
        ))}
      </CardContent>
      <ConnectDialog open={adding} onOpenChange={setAdding} />
    </Card>
  )
}

const statusLabels: Record<string, string> = {
  ready: "Ready",
  incomplete: "Missing permissions",
  failed: "Refused",
}

function ConnectionRow({
  connection: c,
  mayManage,
}: {
  connection: Connection
  mayManage: boolean
}) {
  const check = useMutation(useConnectionsCheck)
  const [replacing, setReplacing] = useState(false)
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
    <div className="grid gap-3 rounded-lg border px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="grid min-w-0 flex-1 gap-0.5">
          <span className="flex items-center gap-2 font-medium">
            {c.name}
            <span className="inline-flex items-center gap-1.5 text-xs font-normal text-muted-foreground">
              <span
                aria-hidden="true"
                className={cn(
                  "inline-block h-1.5 w-3 rounded-full",
                  c.status === "ready" && "bg-overdye",
                  c.status === "incomplete" && "bg-weld",
                  c.status === "failed" && "bg-destructive"
                )}
              />
              {statusLabels[c.status] ?? c.status}
            </span>
          </span>
          <span className="truncate text-sm text-muted-foreground">
            {cloudLabel(c.cloud)}
            {c.account && (
              <>
                {" · "}
                <code className="font-mono text-xs">{c.account}</code>
              </>
            )}
            {email && (
              <>
                {" · "}
                <code className="font-mono text-xs">{email}</code>
              </>
            )}
          </span>
        </div>
        {c.checkedAt && (
          <span className="text-xs text-muted-foreground">
            Checked{" "}
            <time dateTime={c.checkedAt}>{formatDate(c.checkedAt)}</time>
          </span>
        )}
        {mayManage && (
          <div className="flex gap-1">
            <Button
              variant="ghost"
              size="sm"
              className="rounded-full"
              disabled={check.isPending}
              onClick={() => void recheck()}
            >
              {check.isPending ? <Spinner /> : <RefreshCwIcon />}
              Check again
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="rounded-full"
              onClick={() => setReplacing(true)}
            >
              Replace
            </Button>
            <Remove connection={c} />
          </div>
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
    </div>
  )
}

function Remove({ connection: c }: { connection: Connection }) {
  const remove = useMutation(useConnectionsDelete)
  const invalidate = useInvalidate()
  const [open, setOpen] = useState(false)

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: c.id } })
      invalidate(useConnectionsList)
      toast.success(`Removed ${c.name}`)
    } catch (err) {
      setOpen(false)
      toast.error(messageOf(err, "The connection wasn't removed."))
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="rounded-full text-muted-foreground"
            aria-label={`Remove ${c.name}`}
          />
        }
      >
        <Trash2Icon />
      </AlertDialogTrigger>
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
 * ConnectDialog connects a cloud, or with replacing set, swaps an existing
 * connection's credential for a new one.
 */
function ConnectDialog({
  open,
  onOpenChange,
  replacing,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  replacing?: Connection
}) {
  const create = useMutation(useConnectionsCreate)
  const replace = useMutation(useConnectionsReplace)
  const invalidate = useInvalidate()
  const file = useRef<HTMLInputElement>(null)
  const [cloud, setCloud] = useState<Cloud>(
    (replacing?.cloud as Cloud | undefined) ?? "gcp"
  )
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
  const [error, setError] = useState("")
  const busy = create.isPending || replace.isPending

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

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    const settings =
      cloud === "vercel" && team.trim()
        ? { team_id: team.trim() }
        : cloud === "aws"
          ? {
              region: region.trim(),
              ...(certificate.trim()
                ? { certificate_arn: certificate.trim() }
                : {}),
            }
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
      invalidate(useConnectionsList)
      toast.success(
        c.status === "ready"
          ? `${c.name} is connected and ready`
          : `${c.name} is connected, but missing permissions`
      )
      close(false)
    } catch (err) {
      setError(messageOf(err, "The cloud wasn't connected."))
    }
  }

  const info = clouds[cloud]
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={(e) => void submit(e)} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>
              {replacing
                ? `Replace ${replacing.name}'s credential`
                : "Connect a cloud"}
            </DialogTitle>
            <DialogDescription>
              {replacing
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
                    spacing={0}
                    value={[cloud]}
                    onValueChange={(v) => {
                      if (v[0]) setCloud(v[0] as Cloud)
                    }}
                  >
                    <ToggleGroupItem value="gcp">Google Cloud</ToggleGroupItem>
                    <ToggleGroupItem value="vercel">Vercel</ToggleGroupItem>
                    <ToggleGroupItem value="aws">AWS</ToggleGroupItem>
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
            {cloud === "gcp" ? (
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
                    className="rounded-full"
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
                  A JSON key for a service account in the project you deploy to.
                  It needs Cloud Run Admin, Cloud Build Editor, Artifact
                  Registry Administrator, Storage Admin, Secret Manager Admin,
                  Service Account User and Browser; Loomplane says which are
                  missing.
                </FieldDescription>
              </Field>
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
              className="rounded-full"
              onClick={() => close(false)}
            >
              Cancel
            </Button>
            <Button type="submit" className="rounded-full" disabled={busy}>
              {busy && <Spinner />}
              {replacing ? "Check and replace" : "Check and connect"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
