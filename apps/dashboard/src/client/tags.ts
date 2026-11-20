// Cache tags the hand-written bindings add to generated ones. Each typename
// comes from the generated contract rather than being spelled out, because
// the server names a type after its package when two share a name: a
// deployment is `DeploymentsDeployment`, and the day that changes these
// follow it instead of silently matching nothing.
import { op_deployments_get } from "@/client/forge/loomplane/ops/deployments.get"

const deployment = op_deployments_get.entity

/** Every deployment list; a deployment.updated event invalidates it. */
export const deploymentLists = `${deployment}[]`

/** One deployment, by the `{id}` path parameter. */
export const deploymentItem = `${deployment}:{id}`
