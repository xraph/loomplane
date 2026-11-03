import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudSignInAppsDelete,
  usePlatformCloudSignInAppsList,
  usePlatformCloudSignInAppsSet,
  useConnectionCloudsList,
} from "@/client/forge/loomplane/hooks"
import type { KeylessState, SignInApp } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { CopyButton } from "@/components/keyless-setup"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { cloudLabel } from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { usePeople } from "@/lib/use-runs"

type Dialogue = { app: SignInApp; mode: "edit" | "view" }

const docsBase = "https://loomplane.dev/docs/clouds/cloud-sign-in"

const docsAnchors: Record<string, string> = {
  gcp: "google-cloud",
}

/** cloudSignInDocsUrl links to the docs for registering a cloud's OAuth app. */
export function cloudSignInDocsUrl(cloud: string): string {
  return `${docsBase}#${docsAnchors[cloud] ?? cloud}`
}

const plural = (n: number, one: string, many: string) =>
  `${n} ${n === 1 ? one : many}`

/** signedInWarning says who has to sign in again if the app changes. */
function signedInWarning(app: SignInApp): string {
  const subject = plural(app.connections, "connection", "connections")
  const orgs = plural(app.organizations, "organization", "organizations")
  const verb = app.connections === 1 ? "needs" : "will need"
  return `${subject} in ${orgs} signed in with this app ${verb} to sign in again.`
}

/**
 * CloudSignInApps lists the OAuth apps people sign in to each cloud with and
 * lets a platform owner set or remove them. The setup step reuses it with
 * variant "setup".
 */
export function CloudSignInApps({
  variant,
  onSaved,
}: {
  variant: "page" | "setup"
  /** Called after an app is saved. The setup step uses it to say Continue. */
  onSaved?: () => void
}) {
  const apps = useQuery(usePlatformCloudSignInAppsList)
  const person = usePeople()
  const [open, setOpen] = useState<Dialogue | null>(null)
  // The setup variant opens one cloud's form inline at a time.
  const [expanded, setExpanded] = useState<string | null>(null)

  const state = (app: SignInApp) => {
    if (app.source === "config") return "Set by the deployment · locked"
    if (app.source === "platform") {
      const by = app.updatedBy ? ` by ${person(app.updatedBy).name}` : ""
      const when = app.updatedAt ? ` · ${formatDate(app.updatedAt)}` : ""
      return `Set${by}${when}`
    }
    return "Not set up · people use a token"
  }

  return (
    <>
      {apps.status === "error" && (
        <Alert variant="destructive">
          <AlertDescription>
            {messageOf(apps.error, "Cloud sign-in couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!apps.data && apps.status !== "error" && (
        <Skeleton className="h-40 w-full rounded-xl" />
      )}
      {apps.data && (
        <>
          <CallbackUrl url={apps.data.callbackUrl} />
          {variant === "page" && <KeylessStatus state={apps.data.keyless} />}
          <Card className="gap-0 py-0">
            {apps.data.apps.map((app) => (
              <div key={app.cloud} className="border-b last:border-b-0">
                <div className="flex items-center gap-3 px-4 py-3">
                  <CloudGlyph cloud={app.cloud} />
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">{cloudLabel(app.cloud)}</div>
                    <div className="text-sm text-muted-foreground">
                      {state(app)}
                    </div>
                    {app.problem && (
                      <div className="text-sm text-destructive">
                        {app.problem}
                      </div>
                    )}
                    {app.cloud === "gcp" && (
                      <div className="text-sm text-muted-foreground">
                        While your Google OAuth app is in testing, Google ends
                        sign-ins after 7 days. Publish the app to keep them.
                      </div>
                    )}
                    <a
                      href={cloudSignInDocsUrl(app.cloud)}
                      target="_blank"
                      rel="noreferrer"
                      aria-label={`${cloudLabel(app.cloud)} sign-in setup guide`}
                      className="w-fit text-sm text-muted-foreground underline underline-offset-4"
                    >
                      Setup guide
                    </a>
                  </div>
                  {variant === "setup" ? (
                    app.source !== "config" && (
                      <Button
                        variant={app.source === "none" ? "default" : "outline"}
                        size="sm"
                        aria-expanded={expanded === app.cloud}
                        aria-label={`${app.source === "none" ? "Set up" : "Edit"} ${cloudLabel(app.cloud)}`}
                        onClick={() =>
                          setExpanded(expanded === app.cloud ? null : app.cloud)
                        }
                      >
                        {app.source === "none" ? "Set up" : "Edit"}
                      </Button>
                    )
                  ) : (
                    <>
                      {app.source !== "none" && (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={app.source === "config"}
                          onClick={() => setOpen({ app, mode: "view" })}
                        >
                          View
                        </Button>
                      )}
                      {app.source === "platform" && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setOpen({ app, mode: "edit" })}
                        >
                          Edit
                        </Button>
                      )}
                      {app.source === "none" && (
                        <Button
                          size="sm"
                          onClick={() => setOpen({ app, mode: "edit" })}
                        >
                          Set up
                        </Button>
                      )}
                    </>
                  )}
                </div>
                {variant === "setup" && expanded === app.cloud && (
                  <SetupForm
                    app={app}
                    onClose={() => setExpanded(null)}
                    onSaved={onSaved}
                  />
                )}
              </div>
            ))}
          </Card>
        </>
      )}
      {open?.mode === "view" && (
        <ViewDialog app={open.app} onClose={() => setOpen(null)} />
      )}
      {open?.mode === "edit" && (
        <AppDialog
          app={open.app}
          onClose={() => setOpen(null)}
          onSaved={onSaved}
        />
      )}
    </>
  )
}

function CallbackUrl({ url }: { url: string }) {
  return (
    <Card className="gap-2 px-4 py-3">
      <div className="text-sm font-medium">Callback URL</div>
      <p className="text-sm text-muted-foreground">
        Register this as the redirect URL when you make the app with each cloud.
      </p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 font-mono text-xs">
          {url}
        </code>
        <CopyButton value={url} label="Copy callback URL" />
      </div>
    </Card>
  )
}

/** KeylessState says whether clouds can trust Loomplane's tokens, and why not. */
function KeylessStatus({ state }: { state?: KeylessState }) {
  if (!state) return null
  return (
    <Card className="gap-1 px-4 py-3">
      <div className="text-sm font-medium">
        {state.enabled
          ? `Keyless: on · ${state.issuer}`
          : `Keyless: off · ${state.reason}`}
      </div>
      <p className="text-sm text-muted-foreground">
        A cloud can trust Loomplane directly, so you paste no token and none
        lapses.
      </p>
      {state.enabled && state.reason && (
        <p className="text-sm text-weld">{state.reason}</p>
      )}
    </Card>
  )
}

function ViewDialog({ app, onClose }: { app: SignInApp; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{cloudLabel(app.cloud)} sign-in app</DialogTitle>
          <DialogDescription>
            The secret is kept encrypted and never shown.
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor="view-client-id">Client ID</FieldLabel>
          <Input
            id="view-client-id"
            readOnly
            value={app.clientId ?? ""}
            className="font-mono text-xs"
          />
        </Field>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** useAppEditor holds the form for one app, shared by the dialog and the setup row. */
function useAppEditor(
  app: SignInApp,
  onDone: () => void,
  onSaved?: () => void
) {
  const save = useMutation(usePlatformCloudSignInAppsSet)
  const remove = useMutation(usePlatformCloudSignInAppsDelete)
  const invalidate = useInvalidate()
  const [clientId, setClientId] = useState(app.clientId ?? "")
  const [clientSecret, setClientSecret] = useState("")
  const [slug, setSlug] = useState(app.slug ?? "")
  const [error, setError] = useState("")
  const [confirm, setConfirm] = useState<"replace" | "remove" | null>(null)
  const label = cloudLabel(app.cloud)
  const busy = save.isPending || remove.isPending

  const refresh = () => {
    invalidate(usePlatformCloudSignInAppsList)
    invalidate(useConnectionCloudsList)
  }

  // Rotating only the secret keeps every connection working. A new client ID
  // or a removal signs people out, so it asks first.
  const needsConfirm = (kind: "replace" | "remove") =>
    app.source === "platform" &&
    app.connections > 0 &&
    (kind === "remove" || clientId.trim() !== (app.clientId ?? ""))

  const doSave = async () => {
    setError("")
    try {
      await save.mutateAsync({
        path: { cloud: app.cloud },
        body: {
          clientId: clientId.trim(),
          clientSecret,
          ...(app.cloud === "vercel" ? { slug: slug.trim() } : {}),
        },
      })
      setClientSecret("")
      setConfirm(null)
      refresh()
      toast.success(`${label} sign-in is saved`)
      onSaved?.()
      onDone()
    } catch (err) {
      setConfirm(null)
      setError(messageOf(err, "The app wasn't saved."))
    }
  }

  const doRemove = async () => {
    setError("")
    try {
      await remove.mutateAsync({ path: { cloud: app.cloud } })
      setConfirm(null)
      refresh()
      toast.success(`${label} sign-in is removed`)
      onDone()
    } catch (err) {
      setConfirm(null)
      setError(messageOf(err, "The app wasn't removed."))
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (needsConfirm("replace")) setConfirm("replace")
    else void doSave()
  }

  const askRemove = () =>
    needsConfirm("remove") ? setConfirm("remove") : void doRemove()

  return {
    label,
    busy,
    saving: save.isPending,
    clientId,
    setClientId,
    clientSecret,
    setClientSecret,
    slug,
    setSlug,
    error,
    confirm,
    setConfirm,
    submit,
    doSave,
    doRemove,
    askRemove,
  }
}

type Editor = ReturnType<typeof useAppEditor>

function CloudflareNotes() {
  return (
    <div className="grid gap-1 text-sm text-muted-foreground">
      <a
        href="https://developers.cloudflare.com/fundamentals/oauth/create-an-oauth-client/"
        target="_blank"
        rel="noreferrer"
        className="w-fit underline underline-offset-4"
      >
        Register a Cloudflare OAuth client
      </a>
      <p>
        Choose Authorization Code and Refresh Token, with client_secret_basic
        authentication. Enable Account Settings Read and offline_access, then
        Zone Read and DNS Write for DNS, or Workers R2 Storage Write for R2
        storage.
      </p>
      <p>
        Private apps work for members of their Cloudflare account. Public apps
        need domain verification.
      </p>
    </div>
  )
}

function AppFields({ app, editor }: { app: SignInApp; editor: Editor }) {
  return (
    <FieldGroup>
      <Field>
        <FieldLabel htmlFor="app-client-id">Client ID</FieldLabel>
        <Input
          id="app-client-id"
          value={editor.clientId}
          onChange={(e) => editor.setClientId(e.target.value)}
          className="font-mono text-xs"
          autoComplete="off"
          required
        />
      </Field>
      <Field>
        <FieldLabel htmlFor="app-client-secret">Client secret</FieldLabel>
        <Input
          id="app-client-secret"
          type="password"
          value={editor.clientSecret}
          onChange={(e) => editor.setClientSecret(e.target.value)}
          className="font-mono text-xs"
          autoComplete="new-password"
          required
        />
      </Field>
      {app.cloud === "vercel" && (
        <Field>
          <FieldLabel htmlFor="app-slug">Integration slug</FieldLabel>
          <Input
            id="app-slug"
            value={editor.slug}
            onChange={(e) => editor.setSlug(e.target.value)}
            className="font-mono text-xs"
            autoComplete="off"
            required
          />
          <FieldDescription>
            The last part of your integration's URL on Vercel.
          </FieldDescription>
        </Field>
      )}
    </FieldGroup>
  )
}

function AppDialog({
  app,
  onClose,
  onSaved,
}: {
  app: SignInApp
  onClose: () => void
  onSaved?: () => void
}) {
  const editor = useAppEditor(app, onClose, onSaved)
  const { label, busy, confirm, setConfirm } = editor

  if (confirm) {
    return (
      <Dialog open onOpenChange={(next) => !next && onClose()}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {confirm === "replace" ? "Replace" : "Remove"} the {label} app?
            </DialogTitle>
            <DialogDescription>{signedInWarning(app)}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => setConfirm(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={busy}
              onClick={() =>
                void (confirm === "replace"
                  ? editor.doSave()
                  : editor.doRemove())
              }
            >
              {busy && <Spinner />}
              {confirm === "replace" ? "Replace app" : "Remove app"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )
  }

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={editor.submit} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>{label} sign-in app</DialogTitle>
            <DialogDescription>
              From the OAuth app you made with {label}. The secret is stored
              encrypted and never shown again, so enter it each time you save.
            </DialogDescription>
          </DialogHeader>
          {app.cloud === "cloudflare" && <CloudflareNotes />}
          <AppFields app={app} editor={editor} />
          {editor.error && (
            <Alert variant="destructive">
              <AlertDescription>{editor.error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            {app.source === "platform" && (
              <Button
                type="button"
                variant="ghost"
                className="sm:mr-auto"
                disabled={busy}
                onClick={editor.askRemove}
              >
                Remove
              </Button>
            )}
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {editor.saving && <Spinner />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** SetupForm is the same form as the dialog's, open inside a setup row. */
function SetupForm({
  app,
  onClose,
  onSaved,
}: {
  app: SignInApp
  onClose: () => void
  onSaved?: () => void
}) {
  const editor = useAppEditor(app, onClose, onSaved)
  const { label, busy, confirm, setConfirm } = editor

  if (confirm) {
    return (
      <div className="grid gap-3 border-t px-4 py-3">
        <div className="text-sm font-medium">
          {confirm === "replace" ? "Replace" : "Remove"} the {label} app?
        </div>
        <p className="text-sm text-muted-foreground">{signedInWarning(app)}</p>
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => setConfirm(null)}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={busy}
            onClick={() =>
              void (confirm === "replace" ? editor.doSave() : editor.doRemove())
            }
          >
            {busy && <Spinner />}
            {confirm === "replace" ? "Replace app" : "Remove app"}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <form onSubmit={editor.submit} className="grid gap-4 border-t px-4 py-3">
      {app.cloud === "cloudflare" && <CloudflareNotes />}
      <AppFields app={app} editor={editor} />
      {editor.error && (
        <Alert variant="destructive">
          <AlertDescription>{editor.error}</AlertDescription>
        </Alert>
      )}
      <div className="flex justify-end gap-2">
        {app.source === "platform" && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="mr-auto"
            disabled={busy}
            onClick={editor.askRemove}
          >
            Remove
          </Button>
        )}
        <Button type="button" variant="outline" size="sm" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={busy}>
          {editor.saving && <Spinner />}
          Save
        </Button>
      </div>
    </form>
  )
}
