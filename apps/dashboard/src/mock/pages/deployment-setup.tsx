// Interactive design preview. Choices stay in React state and never call
// manifest, plan, connection or deployment mutation APIs.
import {
  ArrowLeftIcon,
  ArrowRightIcon,
  BoxIcon,
  CheckIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  DatabaseIcon,
  FileCodeIcon,
  GitBranchIcon,
  GlobeIcon,
  LayersIcon,
  RocketIcon,
  Settings2Icon,
} from "lucide-react"
import { useState } from "react"

import { CloudGlyph } from "@/components/cloud-glyph"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateTitle,
} from "@/components/zero-state"
import { cn } from "@/lib/utils"

type Target = "vercel" | "ecs" | "render"
const targets: Record<
  Target,
  { label: string; cloud: string; regions: string[] }
> = {
  vercel: { label: "Vercel", cloud: "vercel", regions: ["Global"] },
  ecs: {
    label: "AWS Fargate",
    cloud: "aws",
    regions: ["us-east-1", "us-west-2"],
  },
  render: {
    label: "Render",
    cloud: "render",
    regions: ["virginia", "frankfurt"],
  },
}
type Service = {
  id: string
  name: string
  path: string
  kind: string
  target: Target
  region: string
  included: boolean
  allowed: Target[]
  build: string
}
const seed: Service[] = [
  {
    id: "dashboard",
    name: "dashboard",
    path: "apps/dashboard",
    kind: "Vite · Static site",
    target: "vercel",
    region: "Global",
    included: true,
    allowed: ["vercel", "render"],
    build: "pnpm build",
  },
  {
    id: "api",
    name: "loomplane",
    path: "cmd/loomplane",
    kind: "Forge · HTTP service",
    target: "ecs",
    region: "us-east-1",
    included: true,
    allowed: ["ecs", "render"],
    build: "go build ./cmd/loomplane",
  },
  {
    id: "worker",
    name: "loomplane-worker",
    path: "cmd/loomplane-worker",
    kind: "Forge · Worker",
    target: "ecs",
    region: "us-east-1",
    included: true,
    allowed: ["ecs", "render"],
    build: "go build ./cmd/loomplane-worker",
  },
]
const sampleAccounts = {
  vercel: ["Acme web", "Acme sandbox"],
  aws: ["Acme production", "Acme sandbox"],
  render: ["Acme services", "Acme sandbox"],
}

export function DeploymentSetupPreview() {
  const [services, setServices] = useState(seed)
  const [environment, setEnvironment] = useState("production")
  const [accounts, setAccounts] = useState<Record<string, string>>({
    vercel: "Acme web",
    aws: "Acme production",
    render: "Acme services",
  })
  const [stage, setStage] = useState<"configure" | "review" | "complete">(
    "configure"
  )
  const [manifestOpen, setManifestOpen] = useState(false)
  const active = services.filter((s) => s.included)
  const needsDatabase = active.some((s) => s.id !== "dashboard")
  const clouds = [...new Set(active.map((s) => targets[s.target].cloud))]
  const update = (id: string, patch: Partial<Service>) => {
    setServices((current) =>
      current.map((s) => (s.id === id ? { ...s, ...patch } : s))
    )
  }
  const envLabel = environment === "production" ? "Production" : "Preview"

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          { label: "Deployment design" },
        ]}
      />
      <Page className="gap-5">
        <PageHeading
          title={stage === "complete" ? "Deployment preview" : "New deployment"}
          actions={
            stage !== "complete" && (
              <Button
                className="rounded-full xl:hidden"
                disabled={active.length === 0}
                onClick={() =>
                  setStage(stage === "configure" ? "review" : "complete")
                }
              >
                {stage === "configure"
                  ? "Review deployment"
                  : "Simulate deployment"}
                <ArrowRightIcon />
              </Button>
            )
          }
          description={
            stage === "configure"
              ? "Choose your services. We'll take care of the rest."
              : stage === "review"
                ? "One last look at where everything will run."
                : "Here's how a successful deployment will look."
          }
          eyebrow={
            <div className="flex items-center gap-2">
              <Badge variant="outline">Design preview</Badge>
              <span className="text-xs text-muted-foreground">
                Sample project and accounts
              </span>
            </div>
          }
        />
        <ol
          aria-label="Deployment steps"
          className="flex flex-wrap items-center gap-3 text-xs"
        >
          {(["configure", "review", "complete"] as const).map((step, index) => (
            <li
              key={step}
              className="flex items-center gap-3"
              aria-current={stage === step ? "step" : undefined}
            >
              {index > 0 && (
                <ChevronRightIcon className="size-3 text-muted-foreground" />
              )}
              <span
                className={cn(
                  "flex items-center gap-2",
                  stage === step ? "font-medium" : "text-muted-foreground"
                )}
              >
                <span
                  className={cn(
                    "flex size-5 items-center justify-center rounded-full border text-[11px]",
                    stage === step &&
                      "border-primary bg-primary text-primary-foreground"
                  )}
                >
                  {index + 1}
                </span>
                {step === "configure"
                  ? "Configure"
                  : step === "review"
                    ? "Review"
                    : "Deploy"}
              </span>
            </li>
          ))}
        </ol>

        {stage === "complete" ? (
          <Card size="sm">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <CircleCheckIcon className="size-4 text-overdye" /> Preview
                complete
              </CardTitle>
              <CardDescription>
                No resources were created. Your choices stayed in this preview.
              </CardDescription>
              <CardAction>
                <Badge variant="secondary">{envLabel}</Badge>
              </CardAction>
            </CardHeader>
            <CardContent className="grid gap-3">
              {active.map((service) => (
                <div
                  key={service.id}
                  className="flex flex-wrap items-center justify-between gap-2 border-t pt-3"
                >
                  <div className="flex items-center gap-2">
                    <CloudGlyph cloud={targets[service.target].cloud} />
                    <span className="font-medium">{service.name}</span>
                  </div>
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <CheckIcon className="size-3 text-overdye" /> Build, deploy
                    and health check <Badge variant="outline">Simulated</Badge>
                  </div>
                </div>
              ))}
            </CardContent>
            <CardFooter className="justify-between gap-3 py-3">
              <span className="text-xs text-muted-foreground">
                In the live flow, URLs and build logs appear here.
              </span>
              <Button
                variant="outline"
                className="rounded-full"
                onClick={() => setStage("configure")}
              >
                <ArrowLeftIcon /> Back to configuration
              </Button>
            </CardFooter>
          </Card>
        ) : (
          <div className="grid min-w-0 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_280px]">
            <div className="grid min-w-0 gap-4">
              <Card size="sm">
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <LayersIcon className="size-4 text-muted-foreground" />{" "}
                    Loomplane
                  </CardTitle>
                  <CardDescription>
                    <span className="inline-flex flex-wrap items-center gap-2 text-xs">
                      <span>xraph/loomplane</span>
                      <span>·</span>
                      <GitBranchIcon className="size-3" /> main{" "}
                      <Badge variant="secondary">Forge detected</Badge>
                    </span>
                  </CardDescription>
                  <CardAction>
                    <Picker
                      label="Environment"
                      value={environment}
                      options={[
                        { value: "production", label: "Production" },
                        { value: "preview", label: "Preview" },
                      ]}
                      disabled={stage === "review"}
                      onChange={setEnvironment}
                    />
                  </CardAction>
                </CardHeader>
              </Card>

              {stage === "configure" ? (
                <Card size="sm">
                  <CardHeader>
                    <CardTitle>Services</CardTitle>
                    <CardDescription className="text-xs">
                      Ready to deploy from your repository.
                    </CardDescription>
                    <CardAction>
                      <Badge variant="secondary">
                        {active.length} selected
                      </Badge>
                    </CardAction>
                  </CardHeader>
                  <CardContent className="grid divide-y">
                    {services.map((service) => (
                      <ServiceSetup
                        key={service.id}
                        service={service}
                        update={(patch) => update(service.id, patch)}
                      />
                    ))}
                    <div className="flex flex-wrap items-center gap-1.5 pt-3 text-xs text-muted-foreground">
                      <FileCodeIcon className="size-3.5" /> loomctl is a
                      command-line tool. Examples are excluded.
                    </div>
                  </CardContent>
                </Card>
              ) : (
                <Card size="sm">
                  <CardHeader>
                    <CardTitle>Deployment plan</CardTitle>
                    <CardDescription className="text-xs">
                      Your services will deploy to these destinations.
                    </CardDescription>
                    <CardAction>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="rounded-full"
                        onClick={() => setStage("configure")}
                      >
                        Edit
                      </Button>
                    </CardAction>
                  </CardHeader>
                  <CardContent className="grid divide-y">
                    {active.map((service) => (
                      <div
                        key={service.id}
                        className="flex flex-wrap items-center justify-between gap-2 py-3 first:pt-0 last:pb-0"
                      >
                        <div className="flex items-center gap-2">
                          <CloudGlyph cloud={targets[service.target].cloud} />
                          <div>
                            <div className="font-medium">{service.name}</div>
                            <div className="text-xs text-muted-foreground">
                              {targets[service.target].label} ·{" "}
                              {accounts[targets[service.target].cloud]} ·{" "}
                              {service.region}
                            </div>
                          </div>
                        </div>
                        <Badge variant="outline">Create</Badge>
                      </div>
                    ))}
                    {needsDatabase && (
                      <div className="flex items-center justify-between gap-2 pt-3">
                        <div className="flex items-center gap-2">
                          <DatabaseIcon className="size-4 text-muted-foreground" />
                          <div>
                            <div className="font-medium">Postgres</div>
                            <div className="text-xs text-muted-foreground">
                              Database for your API and worker
                            </div>
                          </div>
                        </div>
                        <Badge variant="outline">Create</Badge>
                      </div>
                    )}
                  </CardContent>
                </Card>
              )}

              {stage === "configure" && clouds.length > 0 && (
                <Card size="sm">
                  <CardHeader>
                    <CardTitle>Accounts</CardTitle>
                    <CardDescription className="text-xs">
                      Where this environment's services will be installed.
                    </CardDescription>
                  </CardHeader>
                  <CardContent className="grid gap-3 sm:grid-cols-2">
                    {clouds.map((cloud) => (
                      <Field key={cloud}>
                        <FieldLabel className="text-xs">
                          <CloudGlyph cloud={cloud} />{" "}
                          {cloud === "aws"
                            ? "Amazon Web Services"
                            : cloud === "vercel"
                              ? "Vercel"
                              : "Render"}
                          <Badge variant="outline" className="ml-auto">
                            Sample
                          </Badge>
                        </FieldLabel>
                        <Picker
                          label={`${cloud} account`}
                          value={accounts[cloud] ?? ""}
                          options={(
                            sampleAccounts[
                              cloud as keyof typeof sampleAccounts
                            ] ?? []
                          ).map((name) => ({ value: name, label: name }))}
                          onChange={(value) =>
                            setAccounts({ ...accounts, [cloud]: value })
                          }
                        />
                      </Field>
                    ))}
                  </CardContent>
                </Card>
              )}
              {manifestOpen && (
                <Card size="sm">
                  <CardHeader>
                    <CardTitle>Manifest preview</CardTitle>
                    <CardDescription className="text-xs">
                      Service entries from this preview. Account choices belong
                      to the environment.
                    </CardDescription>
                  </CardHeader>
                  <CardContent>
                    <pre className="overflow-x-auto font-mono text-xs leading-relaxed">
                      {manifest(services)}
                    </pre>
                  </CardContent>
                </Card>
              )}
            </div>

            <aside
              className="grid gap-3 xl:sticky xl:top-16"
              aria-label="Deployment summary"
            >
              <Card size="sm">
                <CardHeader>
                  <CardTitle>Ready when you are</CardTitle>
                  <CardDescription className="text-xs">
                    {envLabel} · main
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-3">
                  <div className="flex justify-between text-sm">
                    <span className="text-muted-foreground">Services</span>
                    <span className="tabular-nums">{active.length}</span>
                  </div>
                  <div className="flex justify-between text-sm">
                    <span className="text-muted-foreground">
                      Cloud accounts
                    </span>
                    <span className="tabular-nums">{clouds.length}</span>
                  </div>
                  {needsDatabase && (
                    <div className="flex items-start gap-2 rounded-lg bg-muted/50 p-2.5">
                      <DatabaseIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                      <div>
                        <div className="text-xs font-medium">
                          Postgres required
                        </div>
                        <div className="mt-0.5 text-xs text-muted-foreground">
                          Set up alongside the API and worker.
                        </div>
                      </div>
                    </div>
                  )}
                  {stage === "review" && needsDatabase && (
                    <p className="text-xs text-muted-foreground">
                      Database placement and private connectivity will be
                      confirmed by the planner.
                    </p>
                  )}
                  <Button
                    className="w-full rounded-full"
                    disabled={active.length === 0}
                    onClick={() =>
                      setStage(stage === "configure" ? "review" : "complete")
                    }
                  >
                    {stage === "configure" ? (
                      <>
                        Review deployment <ArrowRightIcon />
                      </>
                    ) : (
                      <>
                        <RocketIcon /> Simulate deployment
                      </>
                    )}
                  </Button>
                  <p className="text-center text-xs text-muted-foreground">
                    {stage === "configure"
                      ? "Review every change before deploying."
                      : "Design preview. No cloud changes."}
                  </p>
                </CardContent>
                <CardFooter className="justify-center py-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    className="rounded-full text-muted-foreground"
                    aria-expanded={manifestOpen}
                    onClick={() => setManifestOpen(!manifestOpen)}
                  >
                    <FileCodeIcon /> {manifestOpen ? "Hide" : "View"} manifest
                  </Button>
                </CardFooter>
              </Card>
              {stage === "review" && (
                <Button
                  variant="ghost"
                  className="rounded-full"
                  onClick={() => setStage("configure")}
                >
                  <ArrowLeftIcon /> Back to configuration
                </Button>
              )}
              {active.length === 0 && (
                <ZeroState>
                  <ZeroStateHeader>
                    <ZeroStateTitle>Select a service</ZeroStateTitle>
                    <ZeroStateDescription>
                      Turn on a service above to include it in this deployment.
                    </ZeroStateDescription>
                  </ZeroStateHeader>
                </ZeroState>
              )}
            </aside>
          </div>
        )}
      </Page>
    </>
  )
}

function ServiceSetup({
  service,
  update,
}: {
  service: Service
  update: (patch: Partial<Service>) => void
}) {
  const [advanced, setAdvanced] = useState(false)
  const target = targets[service.target]
  return (
    <section
      className="grid min-w-0 gap-3 py-3 first:pt-0"
      aria-label={`${service.name} configuration`}
    >
      <div className="flex items-center gap-2.5">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted/30">
          {service.id === "dashboard" ? (
            <GlobeIcon className="size-4" />
          ) : (
            <BoxIcon className="size-4" />
          )}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2">
            <span className="text-sm font-medium">{service.name}</span>
            <span className="text-xs text-muted-foreground">
              {service.kind}
            </span>
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            {service.path}
          </span>
        </div>
        <Switch
          aria-label={`Include ${service.name}`}
          checked={service.included}
          onCheckedChange={(included) => update({ included })}
        />
        {service.included && (
          <Button
            variant="ghost"
            size="icon"
            className="rounded-full"
            aria-label={`Advanced settings for ${service.name}`}
            aria-expanded={advanced}
            onClick={() => setAdvanced(!advanced)}
          >
            <Settings2Icon />
          </Button>
        )}
      </div>
      {service.included && (
        <>
          <div className="grid min-w-0 gap-2 sm:grid-cols-2">
            <Field className="gap-1">
              <FieldLabel className="text-xs text-muted-foreground">
                Infrastructure
              </FieldLabel>
              <Picker
                label={`Infrastructure for ${service.name}`}
                value={service.target}
                options={service.allowed.map((value) => ({
                  value,
                  label: targets[value].label,
                  cloud: targets[value].cloud,
                }))}
                onChange={(value) => {
                  const next = value as Target
                  update({
                    target: next,
                    region: targets[next].regions[0] ?? "Global",
                  })
                }}
              />
            </Field>
            <Field className="gap-1">
              <FieldLabel className="text-xs text-muted-foreground">
                Region
              </FieldLabel>
              <Picker
                label={`Region for ${service.name}`}
                value={service.region}
                options={target.regions.map((value) => ({
                  value,
                  label: value === "Global" ? "Global edge network" : value,
                }))}
                disabled={service.target === "vercel"}
                onChange={(region) => update({ region })}
              />
            </Field>
          </div>
          {advanced && (
            <div className="grid gap-3 rounded-lg bg-muted/40 p-3 sm:grid-cols-2">
              <Field className="gap-1">
                <FieldLabel htmlFor={`build-${service.id}`} className="text-xs">
                  Detected build command
                </FieldLabel>
                <Input
                  id={`build-${service.id}`}
                  value={service.build}
                  readOnly
                  className="font-mono text-xs"
                />
              </Field>
              <Field className="gap-1">
                <FieldLabel className="text-xs">Root directory</FieldLabel>
                <Input
                  aria-label={`Root directory for ${service.name}`}
                  value={service.path}
                  onChange={(e) => update({ path: e.target.value })}
                  className="font-mono text-xs"
                />
              </Field>
            </div>
          )}
        </>
      )}
    </section>
  )
}

function Picker({
  label,
  value,
  options,
  disabled,
  onChange,
}: {
  label: string
  value: string
  options: { value: string; label: string; cloud?: string }[]
  disabled?: boolean
  onChange: (value: string) => void
}) {
  return (
    <Select
      value={value}
      items={options}
      onValueChange={(v) => {
        if (typeof v === "string") onChange(v)
      }}
    >
      <SelectTrigger
        aria-label={label}
        className="w-full min-w-0"
        disabled={disabled}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.cloud && <CloudGlyph cloud={option.cloud} />}
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function manifest(services: Service[]) {
  return `version: 1\nservices:\n${services.map((service) => `  ${service.name}:\n    source: ${service.path}\n    provider: ${service.target === "ecs" ? "ecs-fargate" : service.target}\n    enabled: ${service.included}\n${service.region === "Global" ? "" : `    region: ${service.region}\n`}`).join("")}`
}
