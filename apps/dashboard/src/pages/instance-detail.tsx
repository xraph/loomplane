import { statusOf } from "@/client/errors"
import { useFleetInstancesGet } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { ActiveOperation } from "@/components/fleets/active-operation"
import { InstanceOverview } from "@/components/fleets/instance-overview"
import { InstanceMonitoring } from "@/components/fleets/instance-monitoring"
import { FleetReadState } from "@/components/fleets/read-state"
import { useFleetRefresh } from "@/components/fleets/use-fleet-refresh"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Link } from "@/lib/router"

export function InstanceDetail({
  projectID,
  instanceID,
}: {
  projectID: string
  instanceID: string
}) {
  const instance = useQuery(useFleetInstancesGet, {
    path: { project_id: projectID, id: instanceID },
  })
  useFleetRefresh(
    ![401, 403, 404].includes(statusOf(instance.error)),
    instance.refetch
  )
  const title =
    instance.status === "error"
      ? "Instance"
      : (instance.data?.externalRef ?? "Instance")
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          {
            label: "Instance fleet",
            link: <Link to="/projects/$id/fleet" params={{ id: projectID }} />,
          },
          { label: title },
        ]}
      />
      <Page>
        <PageHeading
          title={<span className="break-words">{title}</span>}
          actions={
            <>
              {instance.data && instance.status !== "error" && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void instance.refetch().catch(() => undefined)}
                >
                  Refresh
                </Button>
              )}
              <Button
                size="sm"
                variant="outline"
                nativeButton={false}
                render={
                  <Link to="/projects/$id/fleet" params={{ id: projectID }} />
                }
              >
                Back to fleet
              </Button>
            </>
          }
        />
        {instance.status === "error" ? (
          <FleetReadState
            projectID={projectID}
            error={instance.error}
            retry={instance.refetch}
          />
        ) : !instance.data ? (
          <FleetReadState
            projectID={projectID}
            loading
            retry={instance.refetch}
          />
        ) : (
          <>
            <ActiveOperation
              key={`${projectID}:${instanceID}`}
              projectID={projectID}
              instance={instance.data}
              onChanged={instance.refetch}
            />
            <InstanceOverview instance={instance.data} />
            <InstanceMonitoring
              key={`${projectID}:${instanceID}`}
              projectID={projectID}
              instanceID={instanceID}
            />
          </>
        )}
      </Page>
    </>
  )
}
