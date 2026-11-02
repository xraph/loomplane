// Fixtures for the canary tests.
import type {
  Canary,
  CanaryVerdict,
  Health,
} from "@/client/forge/loomplane/types"

export const thresholds = {
  errorRate: 0.02,
  errorRateDelta: 0.01,
  minRequests: 200,
  p95LatencyRatio: 1.5,
}

export const health: Health = {
  available: true,
  errors: 0,
  latencyMs: 100,
  requests: 1000,
  statistic: "p95",
}

export function canary(over: Partial<Canary>): Canary {
  return {
    status: "ramping",
    steps: [5, 25, 50, 100],
    holdSeconds: 600,
    thresholds,
    step: 0,
    weight: 5,
    services: [],
    updatedAt: "2026-10-26T10:00:00Z",
    ...over,
  }
}

export function verdict(over: Partial<CanaryVerdict>): CanaryVerdict {
  return {
    id: "d/web/0/0",
    actionId: "web",
    service: "web",
    attempt: 0,
    step: 0,
    weight: 5,
    verdict: "pass",
    reason: "",
    baseline: health,
    baselineFallback: false,
    candidate: health,
    decidedAt: "2026-10-26T10:10:00Z",
    deploymentId: "d",
    orgId: "o",
    projectId: "p",
    retry: 0,
    thresholds,
    windowStart: "2026-10-26T10:00:00Z",
    windowEnd: "2026-10-26T10:10:00Z",
    ...over,
  }
}
