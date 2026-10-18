import {
  useProjectsCreate as createProject,
  useProjectsGet as getProject,
  useManifestsSave as saveManifest,
  usePlansCreate as createPlan,
  usePlansGet as getPlan,
  usePlansApprove as approvePlan,
  useDeploymentsForPlan as getDeployment,
  useBackupPoliciesSet as setPolicy,
} from "@/client/forge/loomplane/hooks"
import { statusOf } from "@/client/errors"
import type { Plan } from "@/client/forge/loomplane/types"

// The Deploy action authorizes one known recipe. A changed plan goes through
// the ordinary review screen instead of acquiring broader approval here.
export function assertPostgresPlan(
  plan: Plan,
  project: string,
  manifest: string,
  target: string
) {
  const action = plan.actions[0]
  if (
    !plan.catalog ||
    !plan.executable ||
    plan.status !== "ready" ||
    plan.projectId !== project ||
    plan.manifestId !== manifest ||
    plan.environment !== "production" ||
    plan.sources.length ||
    plan.problems.length ||
    plan.warnings.some((w) => w.code !== "shared-host-storage") ||
    plan.actions.length !== 1 ||
    action.op !== "create" ||
    action.subject !== "resource" ||
    action.provider !== "server" ||
    action.name !== "database" ||
    action.connectionId !== target ||
    action.resource?.kind !== "postgres" ||
    action.resource.version !== "17" ||
    action.resource.target !== target ||
    !action.resource.catalog?.image
  ) {
    throw new Error(
      "The plan differs from this PostgreSQL deployment. Open the project to review it."
    )
  }
}
const delay = () => new Promise((resolve) => window.setTimeout(resolve, 1000))
async function waitFor<T>(
  read: () => Promise<T>,
  ready: (value: T) => boolean,
  label: string,
  retryMissing = false
): Promise<T> {
  const deadline = Date.now() + 240_000
  while (true) {
    try {
      const value = await read()
      if (ready(value)) return value
    } catch (error) {
      if (!retryMissing || statusOf(error) !== 404) throw error
    }
    if (Date.now() > deadline)
      throw new Error(
        `${label} is still running. Open the project to follow its progress.`
      )
    await delay()
  }
}
export async function deployPostgres(
  input: { name: string; target: string; daily: boolean },
  progress: (message: string) => void,
  created: (project: string) => void
) {
  progress("Creating the service project…")
  const project = await createProject({
    body: {
      name: input.name,
      description: "Standalone PostgreSQL service",
      icon: "database",
    },
  })
  created(project.id)
  await waitFor(
    () => getProject({ path: { id: project.id } }).refetch(),
    (p) => p.status === "ready",
    "Project setup"
  )
  progress("Checking the deployment plan…")
  const manifest = await saveManifest({
    path: { id: project.id },
    body: {
      yaml: `version: 1\nresources:\n  database:\n    kind: postgres\n    provider: server\n    version: "17"\n    target: ${JSON.stringify(input.target)}\n`,
    },
  })
  let plan = await createPlan({
    path: { id: project.id },
    body: { environment: "production", catalog: true },
  })
  const planId = plan.id
  plan = await waitFor(
    () => getPlan({ path: { id: planId } }).refetch(),
    (p) => p.status !== "planning",
    "Planning"
  )
  assertPostgresPlan(plan, project.id, manifest.id, input.target)
  progress("Deploying PostgreSQL with persistent storage…")
  await approvePlan({
    path: { id: plan.id },
    body: {
      revision: plan.revision,
      note: "Deploy the selected standalone PostgreSQL recipe",
    },
  })
  const deployment = await waitFor(
    () => getDeployment({ path: { id: plan.id } }).refetch(),
    (d) => ["succeeded", "failed", "cancelled"].includes(d.status),
    "Deployment",
    true
  )
  if (deployment.status !== "succeeded")
    throw new Error(
      deployment.error ||
        "Deployment failed. Open the project for details and retry."
    )
  if (input.daily) {
    progress("Scheduling daily backups…")
    await setPolicy({
      path: { id: project.id },
      body: {
        environment: "production",
        resource: "database",
        everyHours: 24,
        enabled: true,
      },
    })
  }
  progress("PostgreSQL is ready.")
  return project.id
}
