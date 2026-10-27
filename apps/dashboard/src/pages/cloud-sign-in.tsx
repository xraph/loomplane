import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useCloudSignInAppsDelete,
  useCloudSignInAppsList,
  useCloudSignInAppsSet,
  useConnectionCloudsList,
} from "@/client/forge/loomplane/hooks"
import type { KeylessState, SignInApp } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { CopyButton } from "@/components/keyless-setup"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
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
import { useOrganization } from "@/lib/organization"
import { usePeople } from "@/lib/use-runs"

type Dialogue = { app: SignInApp; mode: "edit" | "view" }

/** CloudSignIn sets the OAuth apps people sign in to a cloud with. */
export function CloudSignIn() {
  const { can } = useOrganization()
  const apps = useQuery(useCloudSignInAppsList)
  const person = usePeople()
  const [open, setOpen] = useState<Dialogue | null>(null)
  const manage = can("cloud_account:manage")

  const state = (app: SignInApp) => {
    if (app.source === "config") return "Set in config · locked"
    if (app.source === "organization") {
      const by = app.updatedBy ? ` by ${person(app.updatedBy).name}` : ""
      const when = app.updatedAt ? ` · ${formatDate(app.updatedAt)}` : ""
      return `Set here${by}${when}`
    }
    return "Not set up · people use a token"
  }

  return (
    <>
      <SiteHeader crumbs={[{ label: "Cloud sign-in" }]} />
      <Page>
        <PageHeading
          title="Cloud sign-in"
          description="Let people connect a cloud by signing in, instead of pasting a token. Register an OAuth app with the cloud, then add its client ID and secret here."
        />

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
            <KeylessStatus state={apps.data.keyless} />
            <Card className="gap-0 py-0">
              {apps.data.apps.map((app) => (
                <div
                  key={app.cloud}
                  className="flex items-center gap-3 border-b px-4 py-3 last:border-b-0"
                >
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
                  </div>
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
                  {manage && app.source === "organization" && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setOpen({ app, mode: "edit" })}
                    >
                      Edit
                    </Button>
                  )}
                  {manage && app.source === "none" && (
                    <Button
                      size="sm"
                      onClick={() => setOpen({ app, mode: "edit" })}
                    >
                      Set up
                    </Button>
                  )}
                </div>
              ))}
            </Card>
          </>
        )}
      </Page>
      {open?.mode === "view" && (
        <ViewDialog app={open.app} onClose={() => setOpen(null)} />
      )}
      {open?.mode === "edit" && (
        <AppDialog app={open.app} onClose={() => setOpen(null)} />
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
        Keyless lets a cloud trust Loomplane directly, so there is no token to
        paste or to lapse.
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

function AppDialog({ app, onClose }: { app: SignInApp; onClose: () => void }) {
  const save = useMutation(useCloudSignInAppsSet)
  const remove = useMutation(useCloudSignInAppsDelete)
  const invalidate = useInvalidate()
  const [clientId, setClientId] = useState(app.clientId ?? "")
  const [clientSecret, setClientSecret] = useState("")
  const [slug, setSlug] = useState(app.slug ?? "")
  const [error, setError] = useState("")
  const label = cloudLabel(app.cloud)
  const busy = save.isPending || remove.isPending

  const refresh = () => {
    invalidate(useCloudSignInAppsList)
    invalidate(useConnectionCloudsList)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
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
      refresh()
      toast.success(`${label} sign-in is saved`)
      onClose()
    } catch (err) {
      setError(messageOf(err, "The app wasn't saved."))
    }
  }

  const doRemove = async () => {
    setError("")
    try {
      await remove.mutateAsync({ path: { cloud: app.cloud } })
      refresh()
      toast.success(`${label} sign-in is removed`)
      onClose()
    } catch (err) {
      setError(messageOf(err, "The app wasn't removed."))
    }
  }

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={(e) => void submit(e)} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>{label} sign-in app</DialogTitle>
            <DialogDescription>
              From the OAuth app you made with {label}. The secret is stored
              encrypted and never shown again, so enter it each time you save.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="app-client-id">Client ID</FieldLabel>
              <Input
                id="app-client-id"
                value={clientId}
                onChange={(e) => setClientId(e.target.value)}
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
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
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
                  value={slug}
                  onChange={(e) => setSlug(e.target.value)}
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
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            {app.source === "organization" && (
              <Button
                type="button"
                variant="ghost"
                className="sm:mr-auto"
                disabled={busy}
                onClick={() => void doRemove()}
              >
                Remove
              </Button>
            )}
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {save.isPending && <Spinner />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
