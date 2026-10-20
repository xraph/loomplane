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
import { databaseRecipes, type DatabaseKind } from "@/lib/catalog-recipes"
import {
  isKubernetesPostgresRecipe,
  sameDatabasePlacement,
  type DatabaseProvider,
} from "@/lib/catalog-targets"
import { statusOf } from "@/client/errors"
import type { Plan } from "@/client/forge/loomplane/types"

// The Deploy action authorizes one known recipe. A changed plan goes through
// the ordinary review screen instead of acquiring broader approval here.
export function assertDatabasePlan(
  plan: Plan,
  project: string,
  manifest: string,
  target: string,
  kind: DatabaseKind = "postgres",
  provider: DatabaseProvider = "server",
  expectedPlacement?: Record<string, unknown>
) {
  const action = plan.actions[0]
  if (
    !plan.catalog ||
    !!plan.cloudSyncId ||
    !plan.executable ||
    plan.status !== "ready" ||
    plan.projectId !== project ||
    plan.manifestId !== manifest ||
    plan.environment !== "production" ||
    plan.sources.length ||
    plan.problems.length ||
    plan.warnings.some(
      (w) =>
        w.code !==
        (provider === "kubernetes"
          ? "retained-cluster-storage"
          : "shared-host-storage")
    ) ||
    plan.actions.length !== 1 ||
    action.op !== "create" ||
    !!action.adoptCloudPlacement ||
    !!action.cloudAdoption ||
    action.subject !== "resource" ||
    action.provider !== provider ||
    action.name !== "database" ||
    action.connectionId !== target ||
    action.resource?.kind !== kind ||
    action.resource.version !== databaseRecipes[kind].version ||
    action.resource.target !== target ||
    !action.resource.catalog?.image ||
    (provider === "kubernetes" &&
      (kind !== "postgres" ||
        action.resource.provider !== provider ||
        !isKubernetesPostgresRecipe(action.resource.catalog) ||
        (expectedPlacement &&
          !sameDatabasePlacement(
            action.resource.targetSettings,
            expectedPlacement
          ))))
  ) {
    throw new Error(
      `The plan differs from this ${databaseRecipes[kind].title} deployment. Open the project to review it.`
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
export async function deployDatabase(
  input: {
    name: string
    target: string
    daily: boolean
    kind: DatabaseKind
    provider?: DatabaseProvider
    targetSettings?: Record<string, unknown>
  },
  progress: (message: string) => void,
  created: (project: string) => void
) {
  const recipe = databaseRecipes[input.kind]
  const provider = input.provider ?? "server"
  if (provider === "kubernetes" && input.kind !== "postgres")
    throw new Error("Kubernetes currently supports PostgreSQL only.")
  progress("Creating the service project…")
  const project = await createProject({
    body: {
      name: input.name,
      description: `Standalone ${recipe.title} service`,
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
      yaml: `version: 1\nresources:\n  database:\n    kind: ${input.kind}\n    provider: ${provider}\n    version: "${recipe.version}"\n    target: ${JSON.stringify(input.target)}\n`,
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
  assertDatabasePlan(
    plan,
    project.id,
    manifest.id,
    input.target,
    input.kind,
    provider,
    input.targetSettings
  )
  progress(`Deploying ${recipe.title} with persistent storage…`)
  await approvePlan({
    path: { id: plan.id },
    body: {
      revision: plan.revision,
      note: `Deploy the selected standalone ${recipe.title} recipe`,
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
  progress(`${recipe.title} is ready.`)
  return project.id
}
