import type {
  CloudSQLApplicationNetwork,
  CloudSQLRecoveryNetwork,
} from "@/client/forge/loomplane/types"

const projectPattern = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/
const namePattern = /^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/

export function cloudSQLNetworkValid(route: CloudSQLRecoveryNetwork) {
  const network = route.network.split("/")
  return (
    projectPattern.test(route.consumerProject) &&
    network.length === 5 &&
    network[0] === "projects" &&
    network[1] === route.consumerProject &&
    network[2] === "global" &&
    network[3] === "networks" &&
    namePattern.test(network[4])
  )
}

export function cloudSQLRegionalResourceValid(
  resource: string,
  project: string,
  region: string,
  collection: "subnetworks" | "forwardingRules"
) {
  const parts = resource.split("/")
  return (
    namePattern.test(region) &&
    parts.length === 6 &&
    parts[0] === "projects" &&
    parts[1] === project &&
    parts[2] === "regions" &&
    parts[3] === region &&
    parts[4] === collection &&
    namePattern.test(parts[5])
  )
}

export function cloudSQLApplicationNetworkValid(
  route: CloudSQLApplicationNetwork,
  region: string
) {
  return (
    cloudSQLNetworkValid(route) &&
    cloudSQLRegionalResourceValid(
      route.subnetwork,
      route.consumerProject,
      region,
      "subnetworks"
    )
  )
}
