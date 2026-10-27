// Which projects may use a domain. Not in the mock, which only shows the
// line; the API needs a way to grant a zone, so the line opens a checklist,
// or the domain's page shows the checklist in place.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useDnsZonesGet,
  useDnsZonesUpdate,
  useDomainsOverview,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Zone } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Skeleton } from "@/components/ui/skeleton"

const same = (a: string[], b: string[]) =>
  a.length === b.length && a.every((id) => b.includes(id))

export function ZoneGrants({
  zone,
  inline = false,
}: {
  zone: Zone
  /** The checklist and Save in place, rather than behind the line. */
  inline?: boolean
}) {
  const projects = useQuery(useProjectsList)
  const update = useMutation(useDnsZonesUpdate)
  const invalidate = useInvalidate()
  const [open, setOpen] = useState(false)
  const [picked, setPicked] = useState<string[]>(zone.projectIds)
  const [error, setError] = useState("")
  // In place, the checklist follows the zone until you change it.
  const [granted, setGranted] = useState(zone.projectIds)
  if (inline && !same(granted, zone.projectIds)) {
    setGranted(zone.projectIds)
    setPicked(zone.projectIds)
  }
  const list = projects.data?.projects ?? []
  const names = zone.projectIds.map(
    (id) => list.find((p) => p.id === id)?.name ?? id
  )
  const line = names.length
    ? `Granted to ${names.join(", ")}`
    : "Not granted to a project yet"

  const show = (next: boolean) => {
    if (next) {
      setPicked(zone.projectIds)
      setError("")
    }
    setOpen(next)
  }

  const refresh = () => {
    invalidate(useDomainsOverview)
    invalidate(useDnsZonesGet)
  }

  const save = async () => {
    setError("")
    try {
      // The zone's revision comes from the latest read: a failed TXT
      // check saves on the zone too, and the page refetches after it.
      await update.mutateAsync({
        path: { zoneId: zone.id },
        body: {
          projectIds: picked,
          renewProof: false,
          revision: zone.revision,
        },
      })
      refresh()
      setOpen(false)
    } catch (err) {
      if (statusOf(err) === 409) {
        refresh()
        setError(
          "This domain changed since you opened it. Check the projects and save again."
        )
        return
      }
      setError(messageOf(err, "The projects weren't saved."))
    }
  }

  const checklist =
    projects.status === "error" ? (
      <p className="text-[12.5px] text-destructive">
        {messageOf(projects.error, "Projects couldn't be loaded.")}
      </p>
    ) : !projects.data ? (
      <Skeleton className="h-16 rounded-lg" />
    ) : list.length === 0 ? (
      <p className="text-[12.5px] text-muted-foreground">
        There are no projects in this organization yet.
      </p>
    ) : (
      <ul className="grid max-h-64 gap-1 overflow-y-auto">
        {list.map((p) => (
          <li key={p.id}>
            <label className="flex items-center gap-2.5 rounded-md px-1.5 py-1 hover:bg-muted">
              <Checkbox
                checked={picked.includes(p.id)}
                disabled={update.isPending}
                onCheckedChange={(on) =>
                  setPicked((was) =>
                    on
                      ? [...was.filter((id) => id !== p.id), p.id]
                      : was.filter((id) => id !== p.id)
                  )
                }
              />
              <span className="truncate">{p.name}</span>
            </label>
          </li>
        ))}
      </ul>
    )
  const alert = error && (
    <p role="alert" className="text-[12.5px] text-destructive">
      {error}
    </p>
  )

  if (inline) {
    return (
      <div className="flex flex-col gap-2.5">
        <p className="text-[12.5px] text-muted-foreground">{line}</p>
        {checklist}
        {alert}
        <div className="flex justify-end">
          <Button
            size="sm"
            disabled={
              update.isPending ||
              !projects.data ||
              same(picked, zone.projectIds)
            }
            onClick={() => void save()}
          >
            Save
          </Button>
        </div>
      </div>
    )
  }

  return (
    <Popover open={open} onOpenChange={show}>
      <PopoverTrigger
        render={
          <button
            type="button"
            className="relative z-10 min-w-[150px] rounded-md text-left text-[12.5px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          />
        }
      >
        {line}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <PopoverHeader>
          <PopoverTitle>Projects that can use {zone.name}</PopoverTitle>
          <PopoverDescription>
            Granted projects can point their hostnames at this domain.
          </PopoverDescription>
        </PopoverHeader>
        {checklist}
        {alert}
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={() => show(false)}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={update.isPending || !projects.data}
            onClick={() => void save()}
          >
            Save
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
