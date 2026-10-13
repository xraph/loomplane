import {
  ArrowUpRightIcon,
  EyeIcon,
  EyeOffIcon,
  FileCodeIcon,
  FolderGit2Icon,
  GitBranchIcon,
  PlusIcon,
  SearchIcon,
  SparklesIcon,
} from "lucide-react"
import { type FormEvent, useMemo, useState } from "react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Item, ItemActions, ItemContent, ItemTitle } from "@/components/ui/item"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import {
  Code,
  CopyButton,
  HealthDot,
  ProviderGlyph,
  segmented,
  StatusDot,
  statusText,
} from "@/mock/components/bits"
import { Page, PageHeading } from "@/components/page"
import {
  type GraphSelection,
  resourceIcon,
  ServiceGraph,
} from "@/mock/components/service-graph"
import { LoomThread } from "@/mock/components/loom"
import { ProjectStylePopover } from "@/components/project-style-picker"
import { TopBar } from "@/mock/components/top-bar"
import {
  type Env,
  manifestFor,
  organization,
  type Project,
  person,
  provider,
  type Resource,
  resources,
  type Service,
  services,
  type Variable,
  variables as seedVariables,
} from "@/mock/data"
import { money, relative } from "@/lib/format"
import { to } from "@/mock/router"
import { useShell } from "@/mock/shell"
import { type DeploymentView, useStore } from "@/mock/store"

export function ProjectPage({ id }: { id: string }) {
  const { deployments, now, projects, restyleProject } = useStore()
  const proj = projects.find((p) => p.id === id)
  const { openDeploy } = useShell()
  const [selected, setSelected] = useState<GraphSelection | null>(null)
  const [review, setReview] = useState<Record<string, "added" | "ignored">>({})

  if (!proj) {
    return (
      <>
        <TopBar crumbs={[{ label: "Overview", path: "/" }, { label: id }]} />
        <Page>
          <Empty className="border border-dashed">
            <EmptyHeader>
              <EmptyTitle>No project called {id}</EmptyTitle>
              <EmptyDescription>
                Pick one from the sidebar, or import a repository to make a new
                one.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <a href={to("/")} className={buttonVariants()}>
                Back to overview
              </a>
            </EmptyContent>
          </Empty>
        </Page>
      </>
    )
  }

  const svc = services.filter((s) => s.project === proj.id)
  const res = resources.filter((r) => r.project === proj.id)
  const languages = [...new Set(svc.map((s) => s.language))]
  const pendingReview = res.filter(
    (r) => r.status === "unconfirmed" && !review[r.id]
  )
  const decide = (r: Resource, choice: "added" | "ignored") => {
    setReview((m) => ({ ...m, [r.id]: choice }))
    setSelected(null)
    toast.success(
      choice === "added"
        ? `Added ${r.name} to loomplane.yaml`
        : `Loomplane won't suggest ${r.name} again`,
      {
        description:
          choice === "added"
            ? "The next deploy plans it like any other resource."
            : undefined,
      }
    )
  }

  return (
    <>
      <TopBar
        crumbs={[
          { label: organization.name, path: "/" },
          { label: "Projects" },
          { label: proj.name },
        ]}
      />
      <Page>
        <PageHeading
          title={
            <span className="flex items-center gap-3">
              <ProjectStylePopover
                name={proj.name}
                icon={proj.icon}
                color={proj.color}
                onChange={(style) => restyleProject(proj.id, style)}
              />
              {proj.name}
            </span>
          }
          description={proj.description}
          actions={
            <>
              <Button
                variant="outline"
                className="rounded-full"
                onClick={() => {
                  void navigator.clipboard?.writeText(
                    `loomplane deploy ${proj.id}`
                  )
                  toast.success("Copied the CLI command")
                }}
              >
                Copy CLI
              </Button>
              <Button
                className="rounded-full"
                onClick={() => openDeploy({ project: proj.id })}
              >
                Deploy
              </Button>
            </>
          }
        >
          <div className="mt-2 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            {[
              { icon: FolderGit2Icon, text: `github.com/${proj.repo}` },
              { icon: GitBranchIcon, text: proj.branch },
              {
                icon: FileCodeIcon,
                text: `loomplane.yaml, revision ${proj.manifestRevision}`,
              },
            ].map(({ icon: Icon, text }) => (
              <Badge
                key={text}
                variant="outline"
                className="h-6 gap-1.5 font-normal"
              >
                <Icon />
                {text}
              </Badge>
            ))}
            {languages.map((l) => (
              <Badge key={l} variant="secondary" className="font-normal">
                {l}
              </Badge>
            ))}
          </div>
        </PageHeading>

        <Environments
          envs={proj.envs}
          deployments={deployments.filter((d) => d.project === proj.id)}
          now={now}
        />

        {pendingReview.map((r) => (
          <Alert
            key={r.id}
            className="items-center gap-y-1 rounded-xl border-dashed border-weld/60 bg-[color-mix(in_oklch,var(--weld)_5%,var(--card))] p-4 has-[>svg]:gap-x-3 md:has-[>svg]:grid-cols-[auto_1fr_auto]"
          >
            <SparklesIcon className="text-weld-ink size-5!" />
            <AlertTitle>
              Loomplane thinks {proj.name} uses a {r.kind.toLowerCase()} that
              isn't in loomplane.yaml
            </AlertTitle>
            <AlertDescription className="col-start-2">
              {r.inferred?.evidence[0]}. It won't create anything until you
              confirm.
            </AlertDescription>
            <div className="col-start-2 mt-2 flex gap-2 md:col-start-3 md:row-span-2 md:row-start-1 md:mt-0">
              <Button
                variant="outline"
                className="rounded-full"
                onClick={() => setSelected({ kind: "resource", id: r.id })}
              >
                Review
              </Button>
              <Button
                className="rounded-full"
                onClick={() => decide(r, "added")}
              >
                Add {r.name}
              </Button>
            </div>
          </Alert>
        ))}

        <Tabs defaultValue="graph">
          <TabsList
            variant="line"
            className="mb-2 max-w-full justify-start overflow-x-auto"
          >
            <TabsTrigger value="graph">Graph</TabsTrigger>
            <TabsTrigger value="services">
              Services
              <span className="text-muted-foreground tabular-nums">
                {svc.length}
              </span>
            </TabsTrigger>
            <TabsTrigger value="variables">Variables</TabsTrigger>
            <TabsTrigger value="manifest">Manifest</TabsTrigger>
            <TabsTrigger value="ci">Deploy from CI</TabsTrigger>
          </TabsList>
          <TabsContent value="graph" className="grid gap-3">
            {svc.length ? (
              <>
                <ServiceGraph
                  projectId={proj.id}
                  selected={selected}
                  onSelect={setSelected}
                />
                <p className="text-xs text-muted-foreground">
                  Hover a node to trace what it talks to, click it for details.
                  Dashed links lead to resources nobody has confirmed yet.
                </p>
              </>
            ) : (
              <NothingDiscovered
                repo={proj.repo}
                onDeploy={() => openDeploy({ project: proj.id })}
              />
            )}
          </TabsContent>
          <TabsContent value="services">
            {svc.length ? (
              <ServicesTable
                rows={svc}
                onSelect={(s) => setSelected({ kind: "service", id: s.id })}
              />
            ) : (
              <NothingDiscovered
                repo={proj.repo}
                onDeploy={() => openDeploy({ project: proj.id })}
              />
            )}
          </TabsContent>
          <TabsContent value="variables">
            <Variables projectId={proj.id} />
          </TabsContent>
          <TabsContent value="manifest">
            <Manifest project={proj} />
          </TabsContent>
          <TabsContent value="ci">
            <DeployFromCI project={proj} />
          </TabsContent>
        </Tabs>
      </Page>

      <Sheet
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
      >
        <SheetContent className="w-full gap-0 sm:max-w-md">
          {selected?.kind === "service" && (
            <ServiceDetail
              s={svc.find((x) => x.id === selected.id) as Service}
              latest={deployments.find((d) => d.project === proj.id)}
              onSelect={setSelected}
            />
          )}
          {selected?.kind === "resource" && (
            <ResourceDetail
              r={res.find((x) => x.id === selected.id) as Resource}
              decision={review[selected.id]}
              onDecide={decide}
              onSelect={setSelected}
            />
          )}
        </SheetContent>
      </Sheet>
    </>
  )
}

function Environments({
  envs,
  deployments,
  now,
}: {
  envs: { env: Env; url: string }[]
  deployments: DeploymentView[]
  now: number
}) {
  return (
    <div
      className={cn(
        "grid gap-4",
        envs.length > 1 && "md:grid-cols-2",
        envs.length > 2 && "xl:grid-cols-3"
      )}
    >
      {envs.map((e) => {
        const latest = deployments.find((d) => d.env === e.env)
        return (
          <Card key={e.env} className="min-w-0">
            <CardHeader>
              <CardTitle className="capitalize">{e.env}</CardTitle>
              <CardAction>
                <a
                  href={`https://${e.url}`}
                  target="_blank"
                  rel="noreferrer"
                  className={cn(
                    buttonVariants({ variant: "link", size: "xs" }),
                    "h-auto max-w-44 px-0 text-muted-foreground"
                  )}
                >
                  <span className="truncate font-mono">{e.url}</span>
                  <ArrowUpRightIcon />
                </a>
              </CardAction>
            </CardHeader>
            <CardContent>
              {latest ? (
                <Item
                  size="sm"
                  className="-mx-3 flex-col items-stretch gap-2 py-2"
                  render={<a href={to(`/deployments/${latest.id}`)} />}
                >
                  <LoomThread steps={latest.steps} size="sm" />
                  <ItemContent className="flex-row items-center gap-2 text-xs">
                    <StatusDot status={latest.status} />
                    <span className="truncate">{latest.message}</span>
                    <span className="ml-auto shrink-0 text-muted-foreground">
                      {latest.status === "running" ||
                      latest.status === "waiting"
                        ? statusText[latest.status]
                        : relative(latest.startedAt, now)}
                    </span>
                  </ItemContent>
                </Item>
              ) : (
                <span className="text-xs text-muted-foreground">
                  Not deployed yet
                </span>
              )}
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}

function ServicesTable({
  rows,
  onSelect,
}: {
  rows: Service[]
  onSelect: (s: Service) => void
}) {
  return (
    <Card className="py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Service</TableHead>
            <TableHead>Runs on</TableHead>
            <TableHead className="text-right">Instances</TableHead>
            <TableHead className="text-right">p95</TableHead>
            <TableHead className="text-right">Errors</TableHead>
            <TableHead>Health</TableHead>
            <TableHead>Detected</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((s) => (
            <TableRow
              key={s.id}
              className="cursor-pointer"
              onClick={() => onSelect(s)}
            >
              <TableCell>
                <div className="grid">
                  <span className="font-medium">{s.name}</span>
                  <Code className="text-xs">{s.path}</Code>
                </div>
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <ProviderGlyph id={s.provider} />
                  <span className="grid leading-tight">
                    <span>{s.runtime}</span>
                    <span className="text-xs text-muted-foreground">
                      {s.region}
                    </span>
                  </span>
                </div>
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {s.instances}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {s.p95 ? (
                  `${s.p95}ms`
                ) : (
                  <span className="text-muted-foreground">n/a</span>
                )}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {s.rps ? (
                  `${s.errorRate.toFixed(2)}%`
                ) : (
                  <span className="text-muted-foreground">n/a</span>
                )}
              </TableCell>
              <TableCell>
                <HealthDot health={s.health} />
              </TableCell>
              <TableCell>
                <Tooltip>
                  <TooltipTrigger render={<span className="inline-flex" />}>
                    <Badge
                      variant="outline"
                      className={cn(
                        "font-normal",
                        s.confidence === "medium" && "border-dashed"
                      )}
                    >
                      {s.language}, {s.confidence} confidence
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent className="max-w-xs">
                    {s.evidence.join(". ")}
                  </TooltipContent>
                </Tooltip>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 truncate">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function DetailSection({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="grid gap-3 px-4 py-4">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

function DependencyChips({
  ids,
  onSelect,
}: {
  ids: string[]
  onSelect: (s: GraphSelection) => void
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {ids.map((id) => {
        const r = resources.find((x) => x.id === id)
        const s = services.find((x) => x.id === id)
        return (
          <Button
            key={id}
            variant="outline"
            size="xs"
            className="font-mono"
            onClick={() =>
              onSelect(r ? { kind: "resource", id } : { kind: "service", id })
            }
          >
            {r?.name ?? s?.name ?? id}
          </Button>
        )
      })}
    </div>
  )
}

function ServiceDetail({
  s,
  latest,
  onSelect,
}: {
  s: Service
  latest?: DeploymentView
  onSelect: (s: GraphSelection) => void
}) {
  const usedBy = services.filter((x) => x.dependsOn.includes(s.id))
  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle className="flex items-center gap-2">
          {s.name}
          <HealthDot health={s.health} />
        </SheetTitle>
        <SheetDescription>
          {s.language} service at <Code>{s.path}</Code>, on{" "}
          {provider(s.provider).name} {s.runtime} in {s.region}.
        </SheetDescription>
      </SheetHeader>
      <div className="flex-1 divide-y overflow-y-auto">
        <DetailSection title="Runs on">
          <Facts
            rows={[
              [
                "Cloud",
                <span className="flex items-center gap-2">
                  <ProviderGlyph id={s.provider} />
                  {provider(s.provider).name}
                </span>,
              ],
              ["Runtime", s.runtime],
              ["Region", s.region],
              ["Instances", String(s.instances)],
              ["Size", s.size],
              ...(s.domains?.length
                ? ([["Domains", <Code>{s.domains.join(", ")}</Code>]] as [
                    string,
                    React.ReactNode,
                  ][])
                : []),
            ]}
          />
        </DetailSection>
        {s.rps > 0 && (
          <DetailSection title="Last hour">
            <div className="grid grid-cols-3 gap-3">
              {[
                ["Requests", `${s.rps.toLocaleString()}/s`],
                ["p95", `${s.p95}ms`],
                ["Errors", `${s.errorRate.toFixed(2)}%`],
              ].map(([k, v]) => (
                <Card key={k} size="sm" className="gap-0.5">
                  <CardContent className="grid gap-0.5">
                    <span className="text-xs text-muted-foreground">{k}</span>
                    <span className="text-base font-medium tabular-nums">
                      {v}
                    </span>
                  </CardContent>
                </Card>
              ))}
            </div>
          </DetailSection>
        )}
        <DetailSection title="How Loomplane found it">
          <ul className="grid gap-1.5 text-sm">
            {s.evidence.map((e) => (
              <li key={e} className="flex gap-2">
                <span className="mt-2 size-1 shrink-0 rounded-full bg-muted-foreground" />
                <span className="font-mono text-xs leading-5">{e}</span>
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">
            {s.confidence === "high"
              ? "High confidence: a framework and an entry point both point here."
              : "Medium confidence: confirmed by Tobi when the manifest was written."}
          </p>
        </DetailSection>
        {(s.dependsOn.length > 0 || usedBy.length > 0) && (
          <DetailSection title="Connections">
            {s.dependsOn.length > 0 && (
              <div className="grid gap-1.5">
                <span className="text-sm">Uses</span>
                <DependencyChips ids={s.dependsOn} onSelect={onSelect} />
              </div>
            )}
            {usedBy.length > 0 && (
              <div className="grid gap-1.5">
                <span className="text-sm">Used by</span>
                <DependencyChips
                  ids={usedBy.map((x) => x.id)}
                  onSelect={onSelect}
                />
              </div>
            )}
          </DetailSection>
        )}
        {latest && (
          <DetailSection title="Latest deploy">
            <Item
              variant="outline"
              className="flex-col items-stretch gap-2"
              render={<a href={to(`/deployments/${latest.id}`)} />}
            >
              <LoomThread steps={latest.steps} size="sm" />
              <ItemContent className="flex-row items-center gap-2">
                <StatusDot status={latest.status} />
                <ItemTitle className="truncate font-normal">
                  {latest.message}
                </ItemTitle>
                <ItemActions className="ml-auto">
                  <Code>{latest.sha}</Code>
                </ItemActions>
              </ItemContent>
            </Item>
          </DetailSection>
        )}
      </div>
      <SheetFooter className="flex-row border-t">
        <a
          href={to(latest ? `/deployments/${latest.id}` : "/deployments")}
          className={cn(buttonVariants({ variant: "outline" }), "flex-1")}
        >
          View logs
        </a>
        <Button
          className="flex-1"
          onClick={() =>
            toast.success(`Restarting ${s.name}`, {
              description: "Instances restart one at a time.",
            })
          }
        >
          Restart service
        </Button>
      </SheetFooter>
    </>
  )
}

function ResourceDetail({
  r,
  decision,
  onDecide,
  onSelect,
}: {
  r: Resource
  decision?: "added" | "ignored"
  onDecide: (r: Resource, choice: "added" | "ignored") => void
  onSelect: (s: GraphSelection) => void
}) {
  const Icon = resourceIcon[r.kind]
  const usedBy = services.filter((x) => x.dependsOn.includes(r.id))
  const unconfirmed = r.status === "unconfirmed" && !decision
  const spec = {
    name: r.name,
    type: r.kind.toLowerCase(),
    engine: r.engine,
    target: `${r.provider}/${r.region}`,
    size: r.size,
    managed_by: r.managedBy === "Ctrl Plane" ? "ctrlplane" : "loomplane",
  }
  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle className="flex items-center gap-2">
          <Icon className="size-4 text-muted-foreground" />
          <span className="font-mono">{r.name}</span>
        </SheetTitle>
        <SheetDescription>
          {unconfirmed
            ? `A ${r.kind.toLowerCase()} Loomplane inferred from the code. Nothing exists yet.`
            : `${r.engine} on ${provider(r.provider).name}, ${r.region}.`}
        </SheetDescription>
      </SheetHeader>
      <div className="flex-1 divide-y overflow-y-auto">
        {r.inferred && (
          <DetailSection title="Why Loomplane suggests it">
            <ul className="grid gap-1.5">
              {r.inferred.evidence.map((e) => (
                <li key={e} className="flex gap-2">
                  <span className="mt-2 size-1 shrink-0 rounded-full bg-weld" />
                  <span className="font-mono text-xs leading-5">{e}</span>
                </li>
              ))}
            </ul>
            <p className="text-xs text-muted-foreground">
              Low confidence, so it's a suggestion. Loomplane never provisions a
              resource it inferred until someone confirms it.
            </p>
          </DetailSection>
        )}
        <DetailSection title="Resource">
          <Facts
            rows={[
              ["Type", r.kind],
              ["Engine", r.engine],
              [
                "Cloud",
                <span className="flex items-center gap-2">
                  <ProviderGlyph id={r.provider} />
                  {provider(r.provider).name}, {r.region}
                </span>,
              ],
              ["Size", r.size],
              ["Monthly cost", r.cost ? money(r.cost, true) : "Not running"],
              ["Lifecycle", r.managedBy],
              [
                "Status",
                decision === "added"
                  ? "Added, provisions on the next deploy"
                  : decision === "ignored"
                    ? "Ignored"
                    : r.status[0]?.toUpperCase() + r.status.slice(1),
              ],
            ]}
          />
        </DetailSection>
        {usedBy.length > 0 && (
          <DetailSection title="Used by">
            <DependencyChips
              ids={usedBy.map((x) => x.id)}
              onSelect={onSelect}
            />
          </DetailSection>
        )}
        <DetailSection title="Spec">
          <Card size="sm" className="relative bg-muted/30 py-0">
            <ScrollArea>
              <pre className="p-3 font-mono text-[12px] leading-5">
                {JSON.stringify(spec, null, 2)}
              </pre>
              <ScrollBar orientation="horizontal" />
            </ScrollArea>
            <CopyButton
              value={JSON.stringify(spec, null, 2)}
              label="Copy spec"
              className="absolute top-1.5 right-1.5"
            />
          </Card>
        </DetailSection>
      </div>
      {unconfirmed && (
        <SheetFooter className="flex-row border-t">
          <Button
            variant="outline"
            className="flex-1"
            onClick={() => onDecide(r, "ignored")}
          >
            Ignore
          </Button>
          <Button className="flex-1" onClick={() => onDecide(r, "added")}>
            Add to manifest
          </Button>
        </SheetFooter>
      )}
    </>
  )
}

const sourceAbout: Record<Variable["source"], string> = {
  Vault: "Stored encrypted in Vault. Only deploys read it.",
  Manifest: "Set in loomplane.yaml, so it lives in the repo.",
  Binding: "Written by Loomplane from a resource this service uses.",
}

function Variables({ projectId }: { projectId: string }) {
  const [rows, setRows] = useState(() =>
    seedVariables.filter((v) => v.project === projectId)
  )
  const [query, setQuery] = useState("")
  const [env, setEnv] = useState("all")
  const [shown, setShown] = useState<Set<string>>(new Set())
  const [adding, setAdding] = useState(false)
  const { now } = useStore()

  const visible = useMemo(
    () =>
      rows.filter(
        (v) =>
          (env === "all" || v.envs.includes(env as Env)) &&
          (!query || v.key.toLowerCase().includes(query.toLowerCase()))
      ),
    [rows, env, query]
  )

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-64">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            data-page-search
            placeholder="Search variables"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Search variables"
          />
          <InputGroupAddon align="inline-end">
            <Kbd>/</Kbd>
          </InputGroupAddon>
        </InputGroup>
        <Select
          items={[
            { label: "Every environment", value: "all" },
            { label: "Production", value: "production" },
            { label: "Staging", value: "staging" },
            { label: "Preview", value: "preview" },
          ]}
          value={env}
          onValueChange={(v) => setEnv(String(v))}
        >
          <SelectTrigger size="sm" className="w-44" aria-label="Environment">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Every environment</SelectItem>
            <SelectItem value="production">Production</SelectItem>
            <SelectItem value="staging">Staging</SelectItem>
            <SelectItem value="preview">Preview</SelectItem>
          </SelectContent>
        </Select>
        <Button size="sm" className="ml-auto" onClick={() => setAdding(true)}>
          <PlusIcon />
          Add variable
        </Button>
      </div>
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-[220px]">Key</TableHead>
              <TableHead>Value</TableHead>
              <TableHead className="w-[200px]">Environments</TableHead>
              <TableHead className="w-[100px]">Source</TableHead>
              <TableHead className="w-[150px] text-right">Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visible.map((v) => {
              const id = `${v.key}:${v.envs.join(",")}`
              const reveal = !v.secret || shown.has(id)
              return (
                <TableRow key={id}>
                  <TableCell className="font-mono text-xs font-medium">
                    {v.key}
                  </TableCell>
                  <TableCell className="max-w-0">
                    <div className="flex items-center gap-1">
                      <code className="truncate font-mono text-xs text-muted-foreground">
                        {reveal ? v.value : "•".repeat(24)}
                      </code>
                      {v.secret && (
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          className="text-muted-foreground"
                          aria-label={
                            reveal ? `Hide ${v.key}` : `Reveal ${v.key}`
                          }
                          onClick={() =>
                            setShown((s) => {
                              const next = new Set(s)
                              if (next.has(id)) next.delete(id)
                              else next.add(id)
                              return next
                            })
                          }
                        >
                          {reveal ? <EyeOffIcon /> : <EyeIcon />}
                        </Button>
                      )}
                      <CopyButton value={v.value} label={`Copy ${v.key}`} />
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {v.envs.map((e) => (
                        <Badge
                          key={e}
                          variant="outline"
                          className="font-normal capitalize"
                        >
                          {e}
                        </Badge>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Tooltip>
                      <TooltipTrigger render={<span className="inline-flex" />}>
                        <Badge variant="secondary" className="font-normal">
                          {v.source}
                        </Badge>
                      </TooltipTrigger>
                      <TooltipContent>{sourceAbout[v.source]}</TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell className="text-right text-xs text-muted-foreground">
                    {relative(v.updated, now)} by{" "}
                    {person(v.updatedBy).name.split(" ")[0]}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
        {visible.length === 0 && (
          <p className="border-t px-4 py-8 text-center text-sm text-muted-foreground">
            {rows.length === 0
              ? "No variables yet. Add one here, or Loomplane writes bindings when it provisions a resource."
              : "No variables match."}
          </p>
        )}
      </Card>
      <AddVariableDialog
        open={adding}
        onOpenChange={setAdding}
        onAdd={(v) => {
          setRows((r) => [{ ...v, project: projectId }, ...r])
          toast.success(`Added ${v.key}`, {
            description: "Services pick it up on their next deploy.",
          })
        }}
      />
    </div>
  )
}

function AddVariableDialog({
  open,
  onOpenChange,
  onAdd,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdd: (v: Omit<Variable, "project">) => void
}) {
  const [key, setKey] = useState("")
  const [value, setValue] = useState("")
  const [secret, setSecret] = useState(true)
  const [envs, setEnvs] = useState<string[]>([
    "production",
    "staging",
    "preview",
  ])
  const valid =
    /^[A-Z_][A-Z0-9_]*$/.test(key) && value.length > 0 && envs.length > 0

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!valid) return
    onAdd({
      key,
      value,
      secret,
      envs: envs as Env[],
      source: secret ? "Vault" : "Manifest",
      updated: Date.now(),
      updatedBy: "maya",
    })
    setKey("")
    setValue("")
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>Add variable</DialogTitle>
            <DialogDescription>
              Secrets go to Vault and never show again in full unless someone
              reveals them here.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="var-key">Key</FieldLabel>
              <Input
                id="var-key"
                value={key}
                onChange={(e) =>
                  setKey(
                    e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_")
                  )
                }
                placeholder="FEATURE_FLAGS_URL"
                className="font-mono"
                autoComplete="off"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="var-value">Value</FieldLabel>
              <Input
                id="var-value"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                type={secret ? "password" : "text"}
                className="font-mono"
                autoComplete="off"
              />
            </Field>
            <Field>
              <FieldLabel>Environments</FieldLabel>
              <ToggleGroup
                multiple
                variant="outline"
                spacing={0}
                value={envs}
                onValueChange={setEnvs}
                className="w-full"
              >
                {["production", "staging", "preview"].map((e) => (
                  <ToggleGroupItem
                    key={e}
                    value={e}
                    className={cn(segmented, "flex-1 capitalize")}
                  >
                    {e}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </Field>
            <Field orientation="horizontal">
              <Switch
                id="var-secret"
                checked={secret}
                onCheckedChange={setSecret}
              />
              <div className="grid gap-0.5">
                <Label htmlFor="var-secret">Secret</Label>
                <FieldDescription>
                  Store it in Vault and mask it in the dashboard.
                </FieldDescription>
              </div>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!valid}>
              Add variable
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function Manifest({ project }: { project: Project }) {
  const manifest = manifestFor(project)
  const lines = manifest.trimEnd().split("\n")
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="border-b py-3">
        <CardTitle className="flex items-center gap-2 font-mono text-sm">
          <FileCodeIcon className="size-4 text-muted-foreground" />
          loomplane.yaml
        </CardTitle>
        <CardDescription>
          Written from what Loomplane found in the repo. Edit it there; the next
          deploy reads it.
        </CardDescription>
        <CardAction>
          <CopyButton value={manifest} label="Copy manifest" />
        </CardAction>
      </CardHeader>
      <ScrollArea className="bg-(--terminal) text-(--terminal-foreground)">
        <pre className="py-4 font-mono text-[12.5px] leading-[1.7]">
          {lines.map((l, i) => (
            <div
              key={i}
              className="grid grid-cols-[3.5rem_1fr] hover:bg-white/[0.04]"
            >
              <span className="pr-5 text-right text-(--terminal-muted)/60 select-none">
                {i + 1}
              </span>
              <YamlLine line={l} />
            </div>
          ))}
        </pre>
        <ScrollBar orientation="horizontal" />
      </ScrollArea>
    </Card>
  )
}

/** YamlLine tints comments and keys; enough to scan, no parser needed. */
function YamlLine({ line }: { line: string }) {
  if (line.trimStart().startsWith("#")) {
    return <span className="text-(--terminal-muted) italic">{line}</span>
  }
  const m = line.match(/^(\s*)([\w-]+)(:)(.*)$/)
  if (!m) return <span>{line || " "}</span>
  const [, indent, key, colon, rest] = m
  return (
    <span>
      {indent}
      <span className="text-[color-mix(in_oklch,var(--overdye)_55%,white)]">
        {key}
      </span>
      <span className="text-(--terminal-muted)">{colon}</span>
      <span>{rest}</span>
    </span>
  )
}

/** CodeBlock is a terminal-style snippet with a copy button. */
function CodeBlock({ code }: { code: string }) {
  return (
    <div className="relative overflow-hidden rounded-xl bg-(--terminal) text-(--terminal-foreground)">
      <ScrollArea>
        <pre className="p-5 pr-14 font-mono text-[12.5px] leading-[1.7]">
          {code}
        </pre>
        <ScrollBar orientation="horizontal" />
      </ScrollArea>
      <CopyButton
        value={code}
        label="Copy snippet"
        className="absolute top-3 right-3 text-(--terminal-muted) hover:bg-white/10 hover:text-(--terminal-foreground)"
      />
    </div>
  )
}

function DeployFromCI({ project: p }: { project: Project }) {
  const [hookShown, setHookShown] = useState(false)
  const hook = `https://loomplane.dev/hooks/deploy/${p.id}/lph_7Rz2Qk9mW4xN`
  const secrets = "${{ secrets.LOOMPLANE_TOKEN }}"
  const snippets = [
    {
      value: "actions",
      label: "GitHub Actions",
      file: ".github/workflows/deploy.yml",
      code: [
        "name: Deploy",
        "on:",
        "  push:",
        `    branches: [${p.branch}]`,
        "",
        "jobs:",
        "  deploy:",
        "    runs-on: ubuntu-latest",
        "    steps:",
        "      - uses: actions/checkout@v4",
        "      - uses: loomplane/deploy-action@v1",
        "        with:",
        `          project: ${p.id}`,
        "          environment: production",
        `          token: ${secrets}`,
      ].join("\n"),
    },
    {
      value: "cli",
      label: "CLI",
      file: "Terminal",
      code: [
        "# Install once",
        "curl -fsSL https://loomplane.dev/install.sh | sh",
        "loomplane login",
        "",
        "# Deploy the commit you're on, then follow it",
        `loomplane deploy ${p.id} --env staging`,
        `loomplane logs ${p.id} --env staging --follow`,
        "",
        "# Plan without deploying",
        `loomplane plan ${p.id} --env production`,
      ].join("\n"),
    },
    {
      value: "api",
      label: "REST API",
      file: "HTTP",
      code: [
        `curl -X POST https://loomplane.dev/api/v1/projects/${p.id}/deployments \\`,
        '  -H "Authorization: Bearer $LOOMPLANE_TOKEN" \\',
        '  -H "Content-Type: application/json" \\',
        `  -d '{"environment": "staging", "ref": "${p.branch}"}'`,
      ].join("\n"),
    },
  ]
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <Card className="gap-0 py-0">
        <Tabs defaultValue="actions" className="gap-0">
          <CardHeader className="flex flex-wrap items-center justify-between gap-3 border-b py-3">
            <TabsList>
              {snippets.map((s) => (
                <TabsTrigger key={s.value} value={s.value}>
                  {s.label}
                </TabsTrigger>
              ))}
            </TabsList>
            <CardDescription className="text-xs">
              Same deploys, same plans, same approvals as the dashboard
            </CardDescription>
          </CardHeader>
          {snippets.map((s) => (
            <TabsContent
              key={s.value}
              value={s.value}
              className="grid gap-3 p-(--card-spacing)"
            >
              <span className="font-mono text-xs text-muted-foreground">
                {s.file}
              </span>
              <CodeBlock code={s.code} />
            </TabsContent>
          ))}
        </Tabs>
      </Card>
      <div className="grid min-w-0 gap-6">
        <Card className="min-w-0">
          <CardHeader>
            <CardTitle className="text-sm">Deploy hook</CardTitle>
            <CardDescription>
              A POST to this URL deploys {p.branch} to staging. Anyone with it
              can deploy, so keep it secret.
            </CardDescription>
          </CardHeader>
          <CardContent className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3">
            <InputGroup>
              <InputGroupInput
                readOnly
                aria-label="Deploy hook URL"
                value={
                  hookShown ? hook : hook.replace(/lph_\w+$/, "lph_••••••••••")
                }
                className="font-mono text-xs"
              />
              <InputGroupAddon align="inline-end">
                <Button
                  variant="ghost"
                  size="icon-xs"
                  className="text-muted-foreground"
                  aria-label={hookShown ? "Hide hook" : "Reveal hook"}
                  onClick={() => setHookShown((v) => !v)}
                >
                  {hookShown ? <EyeOffIcon /> : <EyeIcon />}
                </Button>
                <CopyButton value={hook} label="Copy hook URL" />
              </InputGroupAddon>
            </InputGroup>
            <Button
              variant="outline"
              size="sm"
              className="justify-self-start rounded-full"
              onClick={() =>
                toast.success("Made a new deploy hook", {
                  description: "The old URL stops working now.",
                })
              }
            >
              Regenerate
            </Button>
          </CardContent>
        </Card>
        <Card className="min-w-0">
          <CardHeader>
            <CardTitle className="text-sm">Project token</CardTitle>
            <CardDescription>
              CI needs a token that can deploy {p.name} and nothing else. Store
              it as <span className="font-mono">LOOMPLANE_TOKEN</span>.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button
              size="sm"
              className="justify-self-start rounded-full"
              onClick={() =>
                toast.success(`Created a deploy token for ${p.name}`, {
                  description: "It's shown once. Copy it into your CI secrets.",
                })
              }
            >
              Create token
            </Button>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

/** NothingDiscovered is a new project's graph before its first analysis. */
function NothingDiscovered({
  repo,
  onDeploy,
}: {
  repo: string
  onDeploy: () => void
}) {
  return (
    <Empty className="border border-dashed">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FolderGit2Icon />
        </EmptyMedia>
        <EmptyTitle>Nothing discovered yet</EmptyTitle>
        <EmptyDescription>
          Loomplane reads github.com/{repo} on the first deploy and maps the
          services and resources it finds here.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button className="rounded-full" onClick={onDeploy}>
          Deploy to find out
        </Button>
      </EmptyContent>
    </Empty>
  )
}
