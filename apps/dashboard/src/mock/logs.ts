// Logs for a deployment, written from its steps' timing. A line exists once
// its step has reached it, so a running deploy's log grows on its own.
import {
  type PlanChange,
  person,
  project as findProject,
  provider,
  resources,
  services,
  type Service,
} from "@/mock/data"
import { duration } from "@/mock/format"
import type { DeploymentView, StepView } from "@/mock/store"

export type LogLevel = "info" | "warn" | "error"

export type LogLine = {
  id: string
  ts: number
  level: LogLevel
  step: number
  source: string
  text: string
}

type Draft = [LogLevel, string, string]

function count(changes: PlanChange[], action: PlanChange["action"]) {
  return changes.filter((c) => c.action === action).length
}

function linesFor(d: DeploymentView, s: StepView, svc: Service[]): Draft[] {
  const proj = findProject(d.project)
  const repo = proj?.repo ?? d.project
  const clouds = [...new Set(svc.map((x) => x.provider))]
  const res = resources.filter((r) => r.project === d.project && !r.inferred)
  const cost = d.changes.reduce((a, c) => a + c.cost, 0)
  switch (s.step.key) {
    case "revision":
      return [
        ["info", "git", `Fetched ${d.sha} from github.com/${repo}`],
        ["info", "git", `Commit signed by ${person(d.author).name}`],
        ["info", "git", `${d.sha} is reachable from ${d.branch}`],
      ]
    case "manifest":
      return [
        [
          "info",
          "manifest",
          `Read loomplane.yaml (revision ${proj?.manifestRevision ?? 1})`,
        ],
        [
          "info",
          "manifest",
          `Valid: ${svc.length} services, ${res.length} resources`,
        ],
      ]
    case "deps":
      return [
        [
          "info",
          "graph",
          `Ordered ${svc.length + res.length} nodes, no cycles`,
        ],
        ...svc.map((x): Draft => [
          "info",
          "graph",
          `${x.name} needs ${x.dependsOn.map((id) => id.split("/")[1]).join(", ") || "nothing"}`,
        ]),
      ]
    case "connections":
      return clouds.map((c): Draft => [
        "info",
        c,
        `${provider(c).name}: credentials valid for ${provider(c).account}`,
      ])
    case "capabilities":
      return svc.map((x): Draft => [
        "info",
        x.provider,
        `${x.region} runs ${x.runtime} for ${x.name}`,
      ])
    case "plan":
      return [
        [
          "info",
          "planner",
          `Diffed against state revision ${120 + (d.sha.charCodeAt(0) % 9)}`,
        ],
        [
          "info",
          "planner",
          `${count(d.changes, "create")} to create, ${count(d.changes, "update")} to change, ${count(d.changes, "destroy")} to remove`,
        ],
        [
          "info",
          "planner",
          `Monthly cost change ${cost >= 0 ? "+" : "−"}$${Math.abs(cost).toFixed(2)}`,
        ],
      ]
    case "approval":
      if (!d.requiresApproval) {
        return [
          [
            "info",
            "policy",
            "No approval needed: nothing is removed and this isn't production",
          ],
        ]
      }
      if (s.state === "waiting") {
        return [
          [
            "warn",
            "policy",
            `Approval required by policy ${d.env === "production" ? "approval.production" : "approval.destroy"}`,
          ],
          ["info", "policy", "Waiting for an admin to review the plan"],
        ]
      }
      return [
        [
          "warn",
          "policy",
          `Approval required by policy ${d.env === "production" ? "approval.production" : "approval.destroy"}`,
        ],
        [
          "info",
          "policy",
          `Approved by ${person(d.approvedBy ?? "maya").name}`,
        ],
      ]
    case "approved":
      return [
        ["info", "planner", "Approved plan matches current state, no drift"],
      ]
    case "provision": {
      const creates = d.changes.filter(
        (c) => c.action !== "update" || c.kind.includes("queue")
      )
      if (creates.length === 0) {
        return [["info", "ctrlplane", "No resource changes in this plan"]]
      }
      return creates.flatMap((c): Draft[] => [
        [
          "info",
          "ctrlplane",
          `${c.action === "destroy" ? "Removing" : "Creating"} ${c.kind} ${c.name}`,
        ],
        [
          "info",
          "ctrlplane",
          `${c.name} ${c.action === "destroy" ? "removed" : "available"}`,
        ],
      ])
    }
    case "build":
      return svc.flatMap((x): Draft[] => {
        const cmd =
          x.language === "Go"
            ? `go build ./${x.path}`
            : x.language === "Rust"
              ? `cargo build --release -p ${x.name}`
              : "pnpm build"
        const lines: Draft[] = [
          ["info", x.name, `$ ${cmd}`],
          [
            "info",
            x.name,
            `Built in ${(8 + ((x.name.length * 7) % 40)).toFixed(1)}s`,
          ],
        ]
        if (x.language === "TypeScript" && x.kind === "frontend") {
          lines.splice(1, 0, [
            "warn",
            x.name,
            "2 packages use a deprecated peer range",
          ])
        }
        return lines
      })
    case "deploy":
      return svc.flatMap((x): Draft[] => {
        if (x.provider === "vercel") {
          return [
            [
              "info",
              x.name,
              `Vercel deployment ready, promoting to ${x.domains?.[0] ?? "edge"}`,
            ],
          ]
        }
        const n = typeof x.instances === "number" ? x.instances : 1
        return [
          [
            "info",
            x.name,
            `Rolling ${n} ${n === 1 ? "instance" : "instances"} on ${x.runtime}`,
          ],
          ...Array.from({ length: n }, (_, k): Draft => [
            "info",
            x.name,
            `Instance ${k + 1} of ${n} healthy`,
          ]),
        ]
      })
    case "endpoints":
      return svc
        .filter((x) => x.domains?.length)
        .map((x): Draft => [
          "info",
          "dns",
          `${x.domains?.[0]} now serves ${x.name} (${provider(x.provider).name})`,
        ])
    case "health":
      if (s.state === "failed") {
        const api = svc.find((x) => x.kind === "backend") ?? svc[0]
        return [
          [
            "info",
            "verify",
            `GET https://${api?.domains?.[0] ?? "service"}/healthz`,
          ],
          ["error", "verify", "503 Service Unavailable on 3 of 3 instances"],
          [
            "error",
            api?.name ?? "api",
            "panic: migration 0042_prorations not applied",
          ],
          [
            "error",
            "verify",
            "Health check failed after 3 attempts, rolling back",
          ],
          ["info", "ctrlplane", "Rolled back to the previous release"],
        ]
      }
      return [
        ...svc
          .filter((x) => x.kind !== "worker")
          .map((x): Draft => [
            "info",
            "verify",
            `${x.name}: 200 OK, p95 ${x.p95}ms`,
          ]),
        ["info", "verify", "Error rate 0.03%, under the 1% threshold"],
      ]
    case "record":
      return [
        [
          "info",
          "state",
          `Recorded state revision ${121 + (d.sha.charCodeAt(1) % 9)}`,
        ],
      ]
    case "complete":
      return [
        ["info", "loomplane", `Live on ${d.env} in ${duration(d.elapsedMs)}`],
      ]
  }
  return []
}

/** logs returns every line written so far, oldest first. */
export function logs(d: DeploymentView, now: number): LogLine[] {
  const svc = services.filter((s) => s.project === d.project)
  const out: LogLine[] = []
  for (const s of d.steps) {
    const start = s.startedAt
    if (start === undefined) continue
    const drafts = linesFor(d, s, svc)
    const span =
      s.durationMs ??
      (s.state === "running" || s.state === "waiting" ? now - start : 0)
    const total =
      s.state === "running" && s.progress ? (now - start) / s.progress : span
    drafts.forEach(([level, source, text], k) => {
      const ts =
        s.state === "waiting"
          ? start + k * 400
          : start + ((k + 0.5) / drafts.length) * Math.max(total, 300)
      if (ts > now) return
      out.push({
        id: `${d.id}-${s.index}-${k}`,
        ts,
        level,
        step: s.index,
        source,
        text,
      })
    })
  }
  return out
}
