// How each place a service runs uses its scale. The planner copies scale to
// providers that support it and to servers; the deploy adapters then differ:
// some autoscale between min and max, some run exactly min, and a Docker
// server always runs one.

export type ScaleKind = "range" | "fixed" | "one" | "none"

export type ScaleBehaviour = {
  kind: ScaleKind
  /** One sentence on what the provider does with the numbers. */
  text: string
}

const behaviours: Record<string, ScaleBehaviour> = {
  "cloud-run": {
    kind: "range",
    text: "Cloud Run adds instances under load and removes them when it's quiet, between the fewest and the most you set.",
  },
  "azure-container-apps": {
    kind: "range",
    text: "Container Apps adds replicas under load and removes them when it's quiet, between the fewest and the most you set.",
  },
  kubernetes: {
    kind: "range",
    text: "Kubernetes runs the fewest you set. When the most is higher, a horizontal autoscaler adds pods under load, which needs the cluster's autoscaling ready.",
  },
  digitalocean: {
    kind: "fixed",
    text: "App Platform runs exactly the number you set. It doesn't add or remove instances by itself.",
  },
  render: {
    kind: "fixed",
    text: "Render runs exactly the number you set. It doesn't add or remove instances by itself.",
  },
  "ecs-fargate": {
    kind: "fixed",
    text: "ECS runs exactly the number of tasks you set. It doesn't add or remove them by itself.",
  },
  server: {
    kind: "one",
    text: "A server runs one container per service. Use Kubernetes or a managed provider for more.",
  },
  vercel: {
    kind: "none",
    text: "Vercel sizes and scales functions itself, so scale and size in the manifest are ignored.",
  },
}

/** scaleBehaviour says how a provider, by its name, treats a service's scale. */
export function scaleBehaviour(provider?: string): ScaleBehaviour | undefined {
  return provider ? behaviours[provider] : undefined
}
