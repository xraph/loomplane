import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { CloudLoading } from "@/components/cloud-targets/infrastructure-ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Environment, Target } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import { QueryError } from "@/components/query-error"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useOrganization } from "@/lib/organization"
import { adoptServiceTarget } from "@/lib/cloud-targets"
import { placementSettings } from "@/lib/placement"
import { EmptyClouds } from "./workspace"

export function ServiceAdoption({
  projectId,
  environment,
  environments,
  targets,
  onSaved,
}: {
  projectId: string
  environment: Environment
  environments: Environment[]
  targets: Target[]
  onSaved: () => Promise<void>
}) {
  const manifest = useQuery(api.useManifestsCurrent, {
    path: { id: projectId },
  })
  const save = useMutation(api.useManifestsSave)
  const invalidate = useInvalidate()
  const { can } = useOrganization()
  const [choices, setChoices] = useState<Record<string, string>>({})
  const [reviewing, setReviewing] = useState<string | null>(null)
  const [error, setError] = useState("")
  const services = Object.entries(manifest.data?.manifest?.services ?? {})
  const next =
    reviewing && manifest.data?.manifest
      ? adoptServiceTarget(
          manifest.data.manifest,
          environments,
          environment.name,
          reviewing,
          choices[reviewing]
        )
      : null
  const submit = async () => {
    if (!next || !manifest.data) return
    setError("")
    try {
      await save.mutateAsync({
        path: { id: projectId },
        body: { manifest: next, baseRevision: manifest.data.revision },
      })
      invalidate(api.useManifestsCurrent)
      setReviewing(null)
      setChoices({})
      await onSaved()
    } catch (err) {
      setError(messageOf(err, "The service target wasn't saved."))
      if (statusOf(err) === 409) {
        setReviewing(null)
        setChoices({})
        void manifest.refetch()
      }
    }
  }
  if (manifest.status === "error")
    return statusOf(manifest.error) === 404 ? (
      <EmptyClouds
        kind="notice"
        title="No manifest yet"
        description="Save a project manifest before assigning services to named targets."
      />
    ) : (
      <QueryError
        error={manifest.error}
        fallback="Service placement couldn't be loaded."
        retry={manifest.refetch}
      />
    )
  if (!manifest.data)
    return <CloudLoading>Loading service placement...</CloudLoading>
  return (
    <section className="grid gap-2">
      <h3 className="text-sm font-medium">Services in {environment.name}</h3>
      {!services.length ? (
        <EmptyClouds
          title="No services in the manifest"
          description="Add a service to your project manifest, then choose a named target here."
        />
      ) : (
        <div className="divide-y rounded-xl border">
          {services.map(([name, entry]) => {
            const settings = placementSettings(
              manifest.data!.manifest!,
              { section: "services", name, kind: "", entry },
              environment.name
            )
            return (
              <div key={name} className="flex flex-wrap items-center gap-2 p-3">
                <div className="min-w-28 flex-1">
                  <p className="text-sm font-medium">{name}</p>
                  <p className="text-xs break-words text-muted-foreground">
                    {settings.cloudTarget
                      ? `Target: ${settings.cloudTarget}`
                      : `Direct: ${settings.provider || "automatic"}`}
                  </p>
                </div>
                <Select
                  disabled={
                    !can("project:update") || save.isPending || !targets.length
                  }
                  value={choices[name] ?? settings.cloudTarget ?? ""}
                  onValueChange={(e) =>
                    setChoices((c) => ({ ...c, [name]: e ?? "" }))
                  }
                >
                  <SelectTrigger
                    aria-label={`Cloud target for ${name}`}
                    className={`${"w-full min-w-0"} w-40`}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">Choose target</SelectItem>
                    {targets.map((t) => (
                      <SelectItem key={t.id} value={t.name}>
                        {t.displayName || t.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={
                    !choices[name] ||
                    choices[name] === settings.cloudTarget ||
                    !can("project:update") ||
                    save.isPending
                  }
                  onClick={() => {
                    setError("")
                    setReviewing(name)
                  }}
                >
                  Review service edit
                </Button>
              </div>
            )
          })}
        </div>
      )}
      {error && (
        <Alert variant="destructive" className="text-sm text-destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {reviewing && next && (
        <Dialog
          open
          onOpenChange={(open) =>
            !open && !save.isPending && setReviewing(null)
          }
        >
          <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
            <DialogHeader>
              <DialogTitle>
                Assign {reviewing} to {choices[reviewing]}
              </DialogTitle>
              <DialogDescription>
                Save this manifest edit for {environment.name}. Your running
                service stays on its applied account until the next reviewed
                deployment or Cloud sync. Other environments keep their
                effective placement.
              </DialogDescription>
            </DialogHeader>
            <pre
              className="max-h-64 overflow-auto rounded-lg bg-muted p-3 text-xs"
              aria-label="Manifest adoption preview"
            >
              {JSON.stringify(next, null, 2)}
            </pre>
            <p className="text-xs text-muted-foreground">
              Revision {manifest.data.revision}. Conflicting direct placement
              and inherited defaults are removed from this service; other
              entries retain their provider and region.
            </p>
            {error && (
              <Alert variant="destructive" className="text-sm text-destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button
                variant="outline"
                disabled={save.isPending}
                onClick={() => setReviewing(null)}
              >
                Cancel
              </Button>
              <Button disabled={save.isPending} onClick={() => void submit()}>
                Save service target
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </section>
  )
}
