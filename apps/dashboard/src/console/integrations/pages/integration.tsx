import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@forge-go/dashboard-kit/components/alert"
import { Badge } from "@forge-go/dashboard-kit/components/badge"
import {
  DescriptionList,
  DetailLayout,
} from "@forge-go/dashboard-kit/components/detail-layout"
import { PageHeader } from "@forge-go/dashboard-kit/components/page-header"
import { QueryBoundary } from "@forge-go/dashboard-kit/components/query-boundary"
import { Section } from "@forge-go/dashboard-kit/components/section"
import {
  PluginLink,
  type PluginPageProps,
  useQuery,
} from "@forge-go/dashboard-plugin"

import { CopyValue, HealthBadge, SetupBadge, SourceBadge } from "../components"
import { AppSetup } from "../setup-panel"
import type { IntegrationDetail, SignedInPage } from "../types"

export function IntegrationPage({ params }: PluginPageProps) {
  const key = params.key ?? ""
  const detail = useQuery<IntegrationDetail>("oauth.integration", { key })

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <PluginLink
        className="text-sm text-muted-foreground underline-offset-4 hover:underline"
        to="/"
      >
        ← OAuth integrations
      </PluginLink>
      {/* Kept through the refetch a save or removal triggers, so the form
          and its "Saved" note stay put instead of blinking into a skeleton. */}
      <QueryBoundary
        keepPreviousData
        query={detail}
        skeletonRows={8}
        title="OAuth integration"
      >
        {(it) => <Detail integration={it} />}
      </QueryBoundary>
    </section>
  )
}

function Detail({ integration: it }: { integration: IntegrationDetail }) {
  return (
    <>
      <PageHeader
        meta={
          <span className="flex flex-wrap items-center gap-2">
            <SetupBadge integration={it} />
            <SourceBadge source={it.source} />
            <span className="font-mono text-xs text-muted-foreground">
              {it.key}
            </span>
          </span>
        }
        title={it.name}
      />

      {it.problem && (
        <Alert variant="destructive">
          <AlertTitle>{it.provider.name} refused this app</AlertTitle>
          <AlertDescription>
            {it.problem} Saving it again, or a sign-in that works, clears this.
          </AlertDescription>
        </Alert>
      )}

      <DetailLayout
        aside={
          <>
            <ProviderSection integration={it} />
            <DeploymentSection integration={it} />
          </>
        }
        main={
          <>
            <Section title={it.clientId ? "OAuth app" : "Set up an OAuth app"}>
              <div className="p-4">
                <AppSetup integration={it} />
              </div>
            </Section>
            <HealthSection integration={it} />
          </>
        }
      />
    </>
  )
}

function HealthSection({
  integration: it,
}: {
  integration: IntegrationDetail
}) {
  const enabled = it.kind === "cloud" && it.healthAvailable
  const problems = useQuery<SignedInPage>(
    "connections.list",
    { cloud: it.key, health: "problems", limit: 10 },
    { enabled }
  )
  if (!enabled) {
    return null
  }
  const c = it.connections
  return (
    <Section
      actions={
        <PluginLink
          className="text-sm underline underline-offset-4"
          to={`/connections/cloud/${encodeURIComponent(it.key)}`}
        >
          All {it.name} sign-ins
        </PluginLink>
      }
      description={
        c
          ? `${c.total} signed-in ${c.total === 1 ? "connection" : "connections"} across every organization. ${c.healthy} healthy.`
          : undefined
      }
      title="Signed-in connections"
    >
      <QueryBoundary query={problems} skeletonRows={3} title="Connections">
        {(data) =>
          data.items.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">
              {c?.total
                ? "None of them need attention."
                : "Nobody has signed in with it yet."}
            </p>
          ) : (
            <ul className="flex flex-col divide-y">
              {data.items.map((conn) => (
                <li
                  className="flex min-w-0 flex-col gap-1 p-4 text-sm"
                  key={conn.id}
                >
                  <span className="flex flex-wrap items-center gap-2">
                    <HealthBadge health={conn.health} />
                    <span className="font-medium">{conn.name}</span>
                    {conn.account && (
                      <span className="text-muted-foreground">
                        {conn.account}
                      </span>
                    )}
                    <Badge variant="outline">
                      {conn.orgName || conn.orgId}
                    </Badge>
                  </span>
                  {conn.message && (
                    <span className="text-xs wrap-break-word text-muted-foreground">
                      {conn.message}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>
    </Section>
  )
}

function ProviderSection({
  integration: it,
}: {
  integration: IntegrationDetail
}) {
  return (
    <Section title="Provider">
      <div className="p-4">
        <DescriptionList
          items={[
            { term: "Registered with", value: it.provider.name },
            {
              term: "Callback URL",
              value: <CopyValue label="callback URL" value={it.callbackUrl} />,
            },
            {
              term: "Scopes",
              value: it.provider.scopes?.length
                ? it.provider.scopes.join(" ")
                : "Set on the app itself",
            },
            {
              term: "Changing the app",
              value: it.outlivesApp
                ? "Connections keep working"
                : "Connections signed in with the old one must sign in again",
            },
          ]}
        />
      </div>
    </Section>
  )
}

function DeploymentSection({
  integration: it,
}: {
  integration: IntegrationDetail
}) {
  const restart =
    it.kind === "repository" ? "loomplane and loomplane-worker" : "loomplane"
  return (
    <Section
      title={
        it.editable ? "Keep it in the deployment instead" : "Where it's set"
      }
    >
      <div className="flex flex-col gap-2 p-4 text-sm">
        <p className="text-muted-foreground">
          {it.envVars.length > 0
            ? `These variables, or ${it.configKey} in the config file, set it`
            : `${it.configKey} in the config file sets it`}
          {it.editable ? " instead, and win over this page" : ""}. Restart{" "}
          {restart} to apply them.
        </p>
        {it.envVars.length > 0 && (
          <ul className="flex flex-col gap-1">
            {it.envVars.map((v) => (
              <li key={v}>
                <CopyValue label={v} value={v} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </Section>
  )
}
