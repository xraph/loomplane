import { useCallback, useState } from "react"
import { Link } from "@/lib/router"
import { statusOf } from "@/client/errors"
import {
  useFleetsGet,
  useFleetInstancesList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { FleetList, type FleetQuery } from "@/components/fleets/fleet-list"
import { FleetReadState } from "@/components/fleets/read-state"
import { useFleetRefresh } from "@/components/fleets/use-fleet-refresh"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"

export function FleetDetail({ projectID }: { projectID: string }) {
  const fleet = useQuery(useFleetsGet, { path: { project_id: projectID } })
  const allowed = ![401, 403, 404].includes(statusOf(fleet.error))
  useFleetRefresh(allowed, fleet.refetch)
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          {
            label: "Project",
            link: <Link to="/projects/$id" params={{ id: projectID }} />,
          },
          { label: "Instance fleet" },
        ]}
      />
      <Page>
        <PageHeading
          title="Instance fleet"
          description="Review each instance's release and current state."
        />
        {fleet.status === "error" ? (
          <FleetReadState
            projectID={projectID}
            error={fleet.error}
            retry={fleet.refetch}
          />
        ) : !fleet.data ? (
          <FleetReadState projectID={projectID} loading retry={fleet.refetch} />
        ) : (
          <Instances key={projectID} projectID={projectID} />
        )}
      </Page>
    </>
  )
}

function Instances({ projectID }: { projectID: string }) {
  const [query, setQuery] = useState<FleetQuery>({
    after: "",
    status: "",
    external_ref: "",
  })
  const instances = useQuery(useFleetInstancesList, {
    path: { project_id: projectID },
    query: { ...query, limit: 50 },
  })
  const reload = useCallback(() => instances.refetch(), [instances.refetch])
  useFleetRefresh(![401, 403, 404].includes(statusOf(instances.error)), reload)
  if (instances.status === "error") {
    const failure = (
      <FleetReadState
        projectID={projectID}
        error={instances.error}
        retry={reload}
      />
    )
    if ([401, 403, 404].includes(statusOf(instances.error))) return failure
    return (
      <FleetList
        projectID={projectID}
        rows={[]}
        query={query}
        onQuery={setQuery}
        content={failure}
      />
    )
  }
  if (!instances.data)
    return <FleetReadState projectID={projectID} loading retry={reload} />
  const rows = instances.data.items.map((item) => ({
    id: item.id,
    externalRef: item.externalRef,
    status: item.status,
    profileID: item.profileId,
    desiredReleaseID: item.desiredReleaseId,
    observedReleaseID: item.observedReleaseId,
    updatedAt: item.updatedAt,
  }))
  const pending = rows.filter((row) => row.status === "waiting_approval").length
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span>
          {pending
            ? `${pending} ${pending === 1 ? "instance on this page awaits" : "instances on this page await"} policy review.`
            : "Each instance keeps its own environment and release."}
        </span>
        <Button
          size="sm"
          variant="outline"
          onClick={() => void reload().catch(() => undefined)}
        >
          Refresh
        </Button>
      </div>
      <FleetList
        key={`${query.after}:${query.status}:${query.external_ref}`}
        projectID={projectID}
        rows={rows}
        nextCursor={instances.data.nextCursor}
        query={query}
        onQuery={setQuery}
      />
    </>
  )
}
