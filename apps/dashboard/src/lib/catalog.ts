import {
  useProjectsCreate as createProject,
  useProjectsGet as getProject,
  useManifestsSave as saveManifest,
  usePlansCreate as createPlan,
  usePlansGet as getPlan,
  usePlansApprove as approvePlan,
  useDeploymentsForPlan as getDeployment,
  useBackupPoliciesSet as setPolicy,
  useEnvironmentConnectionsChoose as chooseConnection,
} from "@/client/forge/loomplane/hooks"
import { databaseRecipes, type DatabaseKind } from "@/lib/catalog-recipes"
import {
  isKubernetesPostgresRecipe,
  sameDatabasePlacement,
  type DatabaseProvider,
} from "@/lib/catalog-targets"
import { statusOf } from "@/client/errors"
import type { Plan } from "@/client/forge/loomplane/types"
import {
  isManagedDatabaseProvider,
  managedDatabaseCloud,
  sameManagedDatabaseProfile,
  sameCloudSQLSourceEndpoint,
  cloudSQLSourceEndpointValid,
  type ManagedDatabaseReview,
} from "@/lib/managed-catalog"

// The Deploy action authorizes one known recipe. A changed plan goes through
// the ordinary review screen instead of acquiring broader approval here.
export function assertDatabasePlan(
  plan: Plan,
  project: string,
  manifest: string,
  target: string,
  kind: DatabaseKind = "postgres",
  provider: DatabaseProvider = "server",
  expectedPlacement?: Record<string, unknown>,
  expectedManaged?: ManagedDatabaseReview
) {
  const action = plan.actions[0]
  const managed = isManagedDatabaseProvider(provider)
  const matchesRequest = managed
    ? !!expectedManaged &&
      !!expectedManaged.accountIdentity &&
      !!expectedManaged.region &&
      plan.connections[managedDatabaseCloud(provider)] === target &&
      Object.keys(plan.connections).length === 1 &&
      (!action?.connectionId || action.connectionId === target) &&
      action?.region === expectedManaged.region &&
      action.resource?.region === expectedManaged.region &&
      action.resource?.provider === provider &&
      !action.resource.catalog &&
      !action.resource.target &&
      !Object.keys(action.resource.targetSettings ?? {}).length &&
      action.resource.managed?.connectionId === target &&
      action.resource.managed?.accountIdentity ===
        expectedManaged.accountIdentity &&
      sameManagedDatabaseProfile(
        action.resource.managed?.profile,
        expectedManaged.profile
      ) &&
      sameCloudSQLSourceEndpoint(
        action.resource.managed?.cloudSqlPsc,
        expectedManaged.cloudSqlPsc
      )
    : action?.connectionId === target &&
      action.resource?.target === target &&
      !!action.resource.catalog?.image &&
      !action.resource.managed &&
      (provider !== "kubernetes" ||
        (kind === "postgres" &&
          action.resource.provider === provider &&
          isKubernetesPostgresRecipe(action.resource.catalog) &&
          (!expectedPlacement ||
            sameDatabasePlacement(
              action.resource.targetSettings,
              expectedPlacement
            ))))
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
        managed ||
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
    action.resource?.kind !== kind ||
    action.resource.version !== databaseRecipes[kind].version ||
    !matchesRequest
  ) {
    throw new Error(
      `The plan differs from this ${databaseRecipes[kind].title} deployment. Open the project to review it.`
    )
  }
}
const delay = (ms: number) =>
  new Promise((resolve) => window.setTimeout(resolve, ms))
async function waitFor<T>(
  read: () => Promise<T>,
  ready: (value: T) => boolean,
  label: string,
  retryMissing = false,
  options: { timeoutMs?: number; intervalMs?: number } = {}
): Promise<T> {
  const deadline = Date.now() + (options.timeoutMs ?? 240_000)
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
    await delay(options.intervalMs ?? 1000)
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
    managed?: ManagedDatabaseReview
  },
  progress: (message: string) => void,
  created: (project: string) => void
) {
  const recipe = databaseRecipes[input.kind]
  const provider = input.provider ?? "server"
  const managed = input.managed
    ? {
        ...input.managed,
        profile: { ...input.managed.profile },
        ...(input.managed.cloudSqlPsc
          ? { cloudSqlPsc: { ...input.managed.cloudSqlPsc } }
          : {}),
      }
    : undefined
  if (isManagedDatabaseProvider(provider)) {
    if (input.daily)
      throw new Error(
        "Managed backup schedules are not available in Loomplane."
      )
    if (
      input.kind !== "postgres" ||
      !managed?.accountIdentity ||
      !managed.region ||
      managed.profile.provider !== provider ||
      managed.profile.kind !== input.kind ||
      managed.profile.version !== recipe.version
    )
      throw new Error(
        "Choose a checked account, region and managed PostgreSQL profile."
      )
    const psc = managed.profile.access === "cloud-sql-psc-connector"
    if (
      psc
        ? provider !== "cloud-sql" ||
          !cloudSQLSourceEndpointValid(managed.cloudSqlPsc, managed.region)
        : managed.cloudSqlPsc !== undefined
    )
      throw new Error(
        "Choose a canonical VPC and regional manual endpoint for the PSC profile."
      )
  }
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
  if (isManagedDatabaseProvider(provider)) {
    const cloud = managedDatabaseCloud(provider)
    const chosen = await chooseConnection({
      path: { id: project.id, environment: "production", cloud },
      body: { connectionId: input.target },
    })
    const choices =
      chosen && typeof chosen === "object" && "connections" in chosen
        ? chosen.connections
        : undefined
    if (
      !choices ||
      typeof choices !== "object" ||
      Object.entries(choices).find(([key]) => key === cloud)?.[1] !==
        input.target
    )
      throw new Error(
        "The selected cloud account was not saved. Open the project to review its placement."
      )
  }
  const psc = managed?.cloudSqlPsc
  const pscYaml = psc
    ? `    cloud_sql_psc:\n      consumer_project: ${JSON.stringify(psc.consumerProject)}\n      network: ${JSON.stringify(psc.network)}\n      forwarding_rule: ${JSON.stringify(psc.forwardingRule)}\n`
    : ""
  const manifest = await saveManifest({
    path: { id: project.id },
    body: {
      yaml: `version: 1\nresources:\n  database:\n    kind: ${input.kind}\n    provider: ${provider}\n    version: "${recipe.version}"\n${isManagedDatabaseProvider(provider) ? `    region: ${JSON.stringify(managed!.region)}\n${pscYaml}` : `    target: ${JSON.stringify(input.target)}\n`}`,
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
    input.targetSettings,
    managed
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
    true,
    isManagedDatabaseProvider(provider)
      ? { timeoutMs: 45 * 60_000, intervalMs: 5000 }
      : undefined
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
