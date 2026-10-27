// The Settings tab: what Loomplane set up for Octopus and keeps for it.
// Nothing here is a secret: the status names none.
import type { ReactNode } from "react"

import { useOctopusStatus } from "@/client/forge/loomplane/hooks"
import type { OctopusStatus } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { PanelLoading, ReadFailed } from "@/components/octopus/panel-states"
import { usePushRoutes } from "@/components/octopus/use-push-routes"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import type { CatalogPanelProps } from "@/lib/catalog-kinds"
import { isSet, relativeTime, useClock } from "@/lib/domains"
import { gatewayPort } from "@/lib/octopus"
import { useOrganization } from "@/lib/organization"

export function SettingsPanel(props: CatalogPanelProps) {
  const { projectId, environmentId, resource } = props
  const status = useQuery(useOctopusStatus, {
    path: { projectId, environmentId, resource },
  })

  if (status.status === "error" && !status.data)
    return (
      <ReadFailed
        error={status.error}
        retry={status.refetch}
        denied="You can't see this gateway's settings"
        fallback="Octopus's settings couldn't be loaded."
      />
    )
  if (!status.data) return <PanelLoading label="Loading settings" />
  return (
    <div className="grid min-w-0 gap-3.5">
      {status.status === "error" && (
        <QueryError
          error={status.error}
          fallback="Octopus's settings couldn't be loaded."
          retry={status.refetch}
        />
      )}
      <Settings {...props} status={status.data} />
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section
      aria-label={title}
      className="grid gap-1.5 rounded-[14px] border border-border px-4 py-3.5"
    >
      <h3 className="text-sm font-semibold">{title}</h3>
      <div className="grid gap-1.5 text-[13px] leading-normal text-foreground/80">
        {children}
      </div>
    </section>
  )
}

const mono = "font-mono text-[12.5px] text-foreground"

function Settings({
  projectId,
  environmentId,
  resource,
  status,
}: CatalogPanelProps & { status: OctopusStatus }) {
  const { can } = useOrganization()
  const sync = usePushRoutes({ projectId, environmentId, resource })
  const now = useClock()

  return (
    <>
      <Section title="Ports">
        <p>
          Gateway <span className={mono}>{gatewayPort}</span>, routed through
          your domains.
        </p>
        <p>
          Admin and FARP <span className={mono}>9090</span>, private to
          Loomplane.
        </p>
      </Section>

      <Section title="Blocked on your domains">
        <div className="flex flex-wrap gap-1.5">
          {status.attach.blockedPathPrefixes.map((path) => (
            <span
              key={path}
              className="rounded-md bg-muted px-[7px] py-px font-mono text-xs text-foreground/80"
            >
              {path}
            </span>
          ))}
        </div>
      </Section>

      <Section title="Credentials">
        <p>
          Loomplane generated the admin password, session secret, API token and
          FARP token and keeps them in Vault. You don&apos;t need the password:
          Open Octopus admin signs you in.
        </p>
      </Section>

      <Section title="Version">
        {status.version && <p>Octopus {status.version}</p>}
        {isSet(status.startTime) && (
          <p title={new Date(status.startTime).toLocaleString()}>
            Started {relativeTime(status.startTime, now)}
          </p>
        )}
      </Section>

      <Section title="Sync">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="grid flex-[1_1_280px] gap-1">
            <p>
              {isSet(status.syncedAt)
                ? `Last pushed ${relativeTime(status.syncedAt, now)}`
                : "Not pushed yet"}
            </p>
            {status.generation > status.appliedGeneration && (
              <p className="text-xs text-weld-ink">Pushing to Octopus</p>
            )}
            {status.syncError && (
              <p className="text-xs text-destructive">{status.syncError}</p>
            )}
          </div>
          {can("infrastructure:modify") && (
            <Button
              type="button"
              variant="outline"
              disabled={sync.pending}
              onClick={() => void sync.push()}
            >
              Push routes again
            </Button>
          )}
        </div>
      </Section>

      <Section title="Removing Octopus">
        <p>
          Remove <span className={mono}>{resource}</span> from the manifest and
          deploy to delete it. Detach its domains first.
        </p>
      </Section>
    </>
  )
}
