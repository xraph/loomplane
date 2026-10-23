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
import {
  adoptServiceTarget,
  matchCloudTags,
  type CloudChoice,
} from "@/lib/cloud-targets"
import { placementSettings } from "@/lib/placement"
import { TagInput } from "./tag-input"
import { EmptyClouds } from "./workspace"

type Mode = "name" | "tags"

/** sameChoice reports whether a pending choice changes nothing. */
function sameChoice(
  choice: CloudChoice | undefined,
  current: { cloudTarget?: string; cloudTags?: string[] }
): boolean {
  if (choice === undefined) return true
  if (typeof choice === "string")
    return !choice || choice === current.cloudTarget
  return (
    !choice.tags.length ||
    (!current.cloudTarget &&
      choice.tags.join(",") === [...(current.cloudTags ?? [])].sort().join(","))
  )
}

/** TagOutcome previews what the planner will do with a service's tags. */
function TagOutcome({ targets, tags }: { targets: Target[]; tags: string[] }) {
  if (!tags.length) return null
  const match = matchCloudTags(targets, tags)
  if (match.kind === "one")
    return (
      <p className="text-xs text-muted-foreground">
        Runs on{" "}
        <span className="font-medium text-foreground">
          {match.target.displayName || match.target.name}
        </span>
        .
      </p>
    )
  return (
    <Alert className="text-xs">
      <AlertDescription>
        {match.kind === "none"
          ? "No target this project can use carries all of these tags yet, so planning will stop with a finding until one does."
          : `${match.targets.map((t) => t.displayName || t.name).join(", ")} all carry these tags, so planning will stop with a finding. Add a tag only one of them has.`}
      </AlertDescription>
    </Alert>
  )
}

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
  const assignment = useQuery(api.useEnvironmentCloudsGet, {
    path: { id: projectId, environmentId: environment.id },
  })
  const connections = useQuery(api.useConnectionsList)
  const save = useMutation(api.useManifestsSave)
  const invalidate = useInvalidate()
  const { can } = useOrganization()
  const [choices, setChoices] = useState<Record<string, CloudChoice>>({})
  const [modes, setModes] = useState<Record<string, Mode>>({})
  const fallback = assignment.data?.assignment?.defaultPlacement
  const fallbackAccount = (connections.data?.connections ?? []).find(
    (c) => c.id === fallback?.connectionId
  )
  const offered = [...new Set(targets.flatMap((t) => t.tags ?? []))].sort()

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
  const pending = reviewing ? choices[reviewing] : undefined
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
            const mode: Mode =
              modes[name] ?? (settings.cloudTags ? "tags" : "name")
            const choice = choices[name]
            const editable = can("project:update") && !save.isPending
            const tags =
              typeof choice === "object"
                ? choice.tags
                : (settings.cloudTags ?? [])
            const current = settings.cloudTarget
              ? `Target: ${settings.cloudTarget}`
              : settings.cloudTags
                ? `Tags: ${settings.cloudTags.join(", ")}`
                : settings.provider
                  ? `Direct: ${settings.provider}`
                  : fallback
                    ? `Cloud default: ${fallbackAccount?.name ?? fallback.provider}`
                    : "Direct: automatic"
            return (
              <div key={name} className="grid gap-2 p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <div className="min-w-28 flex-1">
                    <p className="text-sm font-medium">{name}</p>
                    <p className="text-xs break-words text-muted-foreground">
                      {current}
                    </p>
                  </div>
                  <Select
                    disabled={!editable || !targets.length}
                    value={mode}
                    onValueChange={(value) => {
                      setModes((m) => ({ ...m, [name]: value as Mode }))
                      setChoices((c) => {
                        const rest = { ...c }
                        delete rest[name]
                        return rest
                      })
                    }}
                  >
                    <SelectTrigger
                      aria-label={`Select ${name}'s target by`}
                      className="w-full min-w-0 sm:w-32"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="name">Target name</SelectItem>
                      <SelectItem value="tags">Target tags</SelectItem>
                    </SelectContent>
                  </Select>
                  {mode === "name" && (
                    <Select
                      disabled={!editable || !targets.length}
                      value={
                        typeof choice === "string"
                          ? choice
                          : (settings.cloudTarget ?? "")
                      }
                      onValueChange={(e) =>
                        setChoices((c) => ({ ...c, [name]: e ?? "" }))
                      }
                    >
                      <SelectTrigger
                        aria-label={`Cloud target for ${name}`}
                        className="w-full min-w-0 sm:w-40"
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
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!editable || sameChoice(choice, settings)}
                    onClick={() => {
                      setError("")
                      setReviewing(name)
                    }}
                  >
                    Review service edit
                  </Button>
                </div>
                {mode === "tags" && (
                  <div className="grid gap-1.5">
                    <TagInput
                      tags={tags}
                      owner="service"
                      label={`Cloud tags for ${name}`}
                      suggestions={offered}
                      disabled={!editable || !targets.length}
                      onChange={(next) =>
                        setChoices((c) => ({ ...c, [name]: { tags: next } }))
                      }
                      help={
                        <>
                          {name} runs on the one target that carries every tag.
                          Press Enter or a comma to add one.
                        </>
                      }
                    />
                    <TagOutcome targets={targets} tags={tags} />
                  </div>
                )}
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
                {typeof pending === "object"
                  ? `Select ${reviewing}'s target by ${pending.tags.join(", ")}`
                  : `Assign ${reviewing} to ${pending}`}
              </DialogTitle>
              <DialogDescription>
                Save this manifest edit for {environment.name}. Your running
                service stays on its applied account until the next reviewed
                deployment or Cloud sync. Other environments keep their
                effective placement.
              </DialogDescription>
            </DialogHeader>
            {typeof pending === "object" && (
              <TagOutcome targets={targets} tags={pending.tags} />
            )}
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
