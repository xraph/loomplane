// Renaming and deleting a Cloud. Neither moves anything that runs.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type { Profile } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudConfirm } from "@/components/cloud-targets/cloud-page"
import {
  CloudError,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { CopyButton } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cloudDeletionError } from "@/lib/cloud-targets"
import { useNavigate } from "@/lib/router"

export function Settings({
  cloud,
  environments,
  onSaved,
}: {
  cloud: Profile
  /** How many environments use the Cloud, which keeps it from deletion. */
  environments?: number
  onSaved: (cloud: Profile) => void
}) {
  const update = useMutation(api.useOrganizationCloudsUpdate)
  const remove = useMutation(api.useOrganizationCloudsDelete)
  const projects = useQuery(api.useProjectsList)
  const navigate = useNavigate()
  const [name, setName] = useState(cloud.name)
  const [error, setError] = useState("")
  const [confirming, setConfirming] = useState(false)
  const [removeError, setRemoveError] = useState("")
  const trimmed = name.trim()
  const save = async () => {
    setError("")
    try {
      onSaved(
        await update.mutateAsync({
          path: { cloudId: cloud.id },
          body: { name: trimmed, revision: cloud.revision },
        })
      )
    } catch (err) {
      setError(
        statusOf(err) === 409
          ? "Someone changed this Cloud meanwhile. Reload, then rename it."
          : messageOf(err, "The name wasn't saved.")
      )
    }
  }
  const destroy = async () => {
    setRemoveError("")
    try {
      await remove.mutateAsync({ path: { cloudId: cloud.id } })
      setConfirming(false)
      await navigate({ to: "/clouds" })
    } catch (err) {
      setRemoveError(cloudDeletionError(err, "Cloud"))
    }
  }
  const inUse = (environments ?? 0) > 0
  const owner = cloud.projectId
    ? `Created in ${projects.data?.projects.find((p) => p?.id === cloud.projectId)?.name ?? "a project"}`
    : "Your organization"
  return (
    <div className="grid max-w-3xl gap-6">
      <section aria-label="General" className="grid gap-2.5">
        <CloudSectionHead
          title="General"
          description="Renaming moves nothing. Environments keep the Cloud they have."
        />
        <form
          className="overflow-hidden rounded-xl bg-card ring-1 ring-border"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <div className="grid gap-1.5 border-b px-4 py-3">
            <label
              htmlFor="cloud-name"
              className="text-[12.5px] text-muted-foreground"
            >
              Name
            </label>
            <Input
              id="cloud-name"
              value={name}
              maxLength={100}
              disabled={update.isPending}
              onChange={(e) => setName(e.target.value)}
              className="max-w-md"
            />
          </div>
          <div className="flex items-center justify-between gap-3 border-b px-4 py-2.5">
            <span className="text-[12.5px] text-muted-foreground">
              Cloud ID
            </span>
            <span className="flex min-w-0 items-center gap-1">
              <code className="truncate font-mono text-[13px]">{cloud.id}</code>
              <CopyButton value={cloud.id} label="Copy Cloud ID" />
            </span>
          </div>
          <div className="flex items-center justify-between gap-3 px-4 py-2.5">
            <span className="text-[12.5px] text-muted-foreground">Owner</span>
            <span className="text-right text-[13px]">{owner}</span>
          </div>
          {error && (
            <div className="border-t px-4 py-3">
              <CloudError message={error} />
            </div>
          )}
          <div className="flex justify-end border-t bg-muted/40 px-4 py-2.5">
            <Button
              type="submit"
              disabled={update.isPending || !trimmed || trimmed === cloud.name}
            >
              Save
            </Button>
          </div>
        </form>
      </section>
      <section aria-label="Danger zone" className="grid gap-2.5">
        <CloudSectionHead
          title="Danger zone"
          description="Each asks you to confirm in a dialog before anything happens."
        />
        <div className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl bg-card px-4 py-3.5 ring-1 ring-destructive/30">
          <div className="grid min-w-0 flex-[1_1_360px] gap-0.5">
            <p className="text-[13px] font-semibold">Delete this Cloud</p>
            <p className="text-[12.5px] leading-relaxed text-muted-foreground">
              {inUse
                ? `${environments} ${environments === 1 ? "environment uses" : "environments use"} it. Point ${environments === 1 ? "it" : "them"} at another Cloud first. `
                : ""}
              Its targets and pools go with it; servers and connected accounts
              stay.
            </p>
          </div>
          <Button
            variant="outline"
            className="text-destructive"
            disabled={inUse || remove.isPending}
            onClick={() => {
              setRemoveError("")
              setConfirming(true)
            }}
          >
            Delete Cloud
          </Button>
        </div>
      </section>
      <CloudConfirm
        open={confirming}
        onOpenChange={setConfirming}
        title={`Delete ${cloud.name}?`}
        confirmLabel="Delete Cloud"
        typeToConfirm={cloud.name}
        pending={remove.isPending}
        onConfirm={() => void destroy()}
        error={removeError && <CloudError message={removeError} />}
      >
        Its targets and pools go with it. Servers and connected accounts stay,
        and nothing that runs on them stops.
      </CloudConfirm>
    </div>
  )
}
