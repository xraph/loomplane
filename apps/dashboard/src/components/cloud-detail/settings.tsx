// Renaming and deleting a Cloud. Neither moves anything that runs.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type { Profile } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { CloudError } from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
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
      await navigate({ to: "/clouds" })
    } catch (err) {
      setRemoveError(cloudDeletionError(err, "Cloud"))
    }
  }
  const inUse = (environments ?? 0) > 0
  return (
    <div className="grid max-w-3xl gap-4">
      <Card size="sm" className="grid gap-2.5 px-4 py-4">
        <Label htmlFor="cloud-name" className="text-[14px] font-semibold">
          Name
        </Label>
        <p className="-mt-1 text-[12.5px] text-muted-foreground">
          Shown on the Clouds list and wherever an environment picks its Cloud.
          Renaming moves nothing.
        </p>
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <Input
            id="cloud-name"
            value={name}
            maxLength={100}
            disabled={update.isPending}
            onChange={(e) => setName(e.target.value)}
            className="min-w-60 flex-1"
          />
          <Button
            type="submit"
            disabled={update.isPending || !trimmed || trimmed === cloud.name}
          >
            Save
          </Button>
        </form>
        <CloudError message={error} />
      </Card>
      <section
        aria-label="Delete this Cloud"
        className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl bg-card px-4 py-4 ring-1 ring-destructive/30"
      >
        <div className="grid min-w-0 flex-[1_1_360px] gap-1">
          <h2 className="text-[14px] font-semibold">Delete this Cloud</h2>
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
          onClick={() => (confirming ? void destroy() : setConfirming(true))}
        >
          {confirming ? "Confirm delete" : "Delete Cloud"}
        </Button>
        {removeError && (
          <div className="basis-full">
            <CloudError message={removeError} />
          </div>
        )}
      </section>
    </div>
  )
}
