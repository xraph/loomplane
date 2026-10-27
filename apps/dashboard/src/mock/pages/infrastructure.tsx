import { EllipsisIcon, PlusIcon, SearchIcon } from "lucide-react"
import { memo, useMemo, useState } from "react"
import {
  Area,
  AreaChart,
  CartesianGrid,
  ReferenceLine,
  XAxis,
  YAxis,
} from "recharts"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Item, ItemContent, ItemDescription } from "@/components/ui/item"
import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { CopyButton, Meter, ProviderGlyph } from "@/mock/components/bits"
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { resourceIcon } from "@/mock/components/service-graph"
import { TopBar } from "@/mock/components/top-bar"
import {
  organization,
  person,
  type Provider,
  providers,
  type ProviderId,
  resources,
  services,
  spendSeries,
} from "@/mock/data"
import { money } from "@/lib/format"

import { to } from "@/mock/router"

export function InfrastructurePage() {
  const [connecting, setConnecting] = useState(false)
  return (
    <>
      <TopBar
        crumbs={[
          { label: organization.name, path: "/" },
          { label: "Infrastructure" },
        ]}
      />
      <Page>
        <PageHeading
          title="Infrastructure"
          description="The clouds Loomplane deploys to, and every resource it runs for you. Ctrl Plane owns the lifecycle; you see one list."
          actions={
            <Button
              className="rounded-full"
              onClick={() => setConnecting(true)}
            >
              <PlusIcon />
              Connect a cloud
            </Button>
          }
        />
        <section className="grid gap-4">
          <SectionHeading
            id="clouds"
            title="Clouds"
            description="Connected accounts, their health and this month's spend"
          />
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            {providers.map((p) => (
              <CloudCard key={p.id} p={p} />
            ))}
          </div>
        </section>
        <SpendChart />
        <Resources />
      </Page>
      <ConnectDialog open={connecting} onOpenChange={setConnecting} />
    </>
  )
}

function CloudCard({ p }: { p: Provider }) {
  const [disconnecting, setDisconnecting] = useState(false)
  const svc = services.filter((s) => s.provider === p.id)
  const res = resources.filter(
    (r) => r.provider === p.id && r.status !== "unconfirmed"
  )
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ProviderGlyph id={p.id} className="size-6 text-[10px]" />
          {p.name}
        </CardTitle>
        <CardDescription className="flex items-center gap-1.5">
          <span
            className={cn(
              "size-1.5 rounded-full",
              p.status === "connected" ? "bg-overdye" : "bg-weld"
            )}
          />
          {p.status === "connected" ? "Connected" : "Degraded"}
          <span className="truncate font-mono text-xs">{p.account}</span>
        </CardDescription>
        <CardAction>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`${p.name} actions`}
                />
              }
            >
              <EllipsisIcon />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onClick={() =>
                  toast.success(
                    `${p.name} answered in ${p.status === "connected" ? 182 : 2410}ms`
                  )
                }
              >
                Test connection
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() =>
                  toast("Credential rotation is part of the full app")
                }
              >
                Rotate credentials
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setDisconnecting(true)}
              >
                Disconnect
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-4">
        {p.note && <p className="text-xs text-weld-ink">{p.note}</p>}
        <div className="grid grid-cols-2 gap-3 text-sm">
          <div className="grid gap-0.5">
            <span className="text-xs text-muted-foreground">Services</span>
            <span className="tabular-nums">{svc.length}</span>
          </div>
          <div className="grid gap-0.5">
            <span className="text-xs text-muted-foreground">Resources</span>
            <span className="tabular-nums">{res.length}</span>
          </div>
        </div>
        <div className="grid gap-1.5">
          <div className="flex items-baseline justify-between text-sm">
            <span className="tabular-nums">{money(p.spend)}</span>
            <span className="text-xs text-muted-foreground">
              of {money(p.budget)} this month
            </span>
          </div>
          <Meter
            value={(p.spend / p.budget) * 100}
            label={`${p.name} spend against budget`}
          />
        </div>
        <div className="flex flex-wrap gap-1">
          {p.regions.map((r) => (
            <Badge key={r} variant="outline" className="font-normal">
              {r}
            </Badge>
          ))}
        </div>
      </CardContent>
      <CardFooter className="mt-auto text-xs text-muted-foreground">
        Connected by {person(p.connectedBy).name}
      </CardFooter>

      <AlertDialog open={disconnecting} onOpenChange={setDisconnecting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disconnect {p.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              {svc.length} {svc.length === 1 ? "service runs" : "services run"}{" "}
              on {p.name}. They keep running, but Loomplane can't deploy, scale
              or repair them until you connect it again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep connected</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                setDisconnecting(false)
                toast("Disconnecting isn't mocked, so nothing changed")
              }}
            >
              Disconnect
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

const spendConfig = {
  total: { label: "Spend", color: "var(--foreground)" },
} satisfies ChartConfig

const SpendChart = memo(function SpendChart() {
  const budget = providers.reduce((a, p) => a + p.budget, 0)
  const spent = spendSeries.at(-1)?.total ?? 0
  const day = spendSeries.length
  const now = new Date()
  const monthDays = new Date(now.getFullYear(), now.getMonth() + 1, 0).getDate()
  const forecast = Math.round((spent / day) * monthDays)
  return (
    <Card id="spend" className="scroll-mt-16">
      <CardHeader>
        <CardTitle>Spend this month</CardTitle>
        <CardDescription>
          {money(spent)} so far across every cloud. At this rate the month ends
          near {money(forecast)}, {forecast > budget ? "over" : "under"} the{" "}
          {money(budget)} budget.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ChartContainer
          config={spendConfig}
          className="aspect-auto h-48 w-full"
        >
          <AreaChart data={spendSeries} margin={{ left: 8, right: 8, top: 8 }}>
            <CartesianGrid vertical={false} />
            <XAxis
              dataKey="day"
              tickLine={false}
              axisLine={false}
              tickMargin={8}
              tickFormatter={(d: number) => `Day ${d}`}
              minTickGap={32}
            />
            <YAxis
              tickLine={false}
              axisLine={false}
              width={52}
              tickFormatter={(v: number) => money(v)}
              domain={[0, Math.ceil(budget / 1000) * 1000]}
            />
            <ReferenceLine
              y={budget}
              stroke="var(--weld)"
              strokeDasharray="4 4"
              label={{
                value: "Budget",
                position: "insideTopLeft",
                fill: "var(--muted-foreground)",
                fontSize: 11,
              }}
            />
            <ChartTooltip
              cursor={false}
              content={
                <ChartTooltipContent
                  indicator="line"
                  labelFormatter={(_, p) => `Day ${p?.[0]?.payload?.day ?? ""}`}
                  formatter={(v) => money(Number(v))}
                />
              }
            />
            <Area
              dataKey="total"
              type="monotone"
              stroke="var(--color-total)"
              strokeWidth={1.5}
              fill="var(--color-total)"
              fillOpacity={0.06}
              isAnimationActive={false}
            />
          </AreaChart>
        </ChartContainer>
      </CardContent>
    </Card>
  )
})

function Resources() {
  const [query, setQuery] = useState("")
  const [cloud, setCloud] = useState<"all" | ProviderId>("all")
  const rows = useMemo(
    () =>
      resources.filter(
        (r) =>
          (cloud === "all" || r.provider === cloud) &&
          (!query ||
            `${r.name} ${r.kind} ${r.engine} ${r.project}`
              .toLowerCase()
              .includes(query.toLowerCase()))
      ),
    [cloud, query]
  )
  const total = rows.reduce((a, r) => a + r.cost, 0)
  return (
    <section className="grid gap-3">
      <SectionHeading
        id="resources"
        title="Resources"
        description="Databases, caches, queues and buckets, across every project."
      />
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-64">
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
          <InputGroupInput
            data-page-search
            placeholder="Search resources"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Search resources"
          />
          <InputGroupAddon align="inline-end">
            <Kbd>/</Kbd>
          </InputGroupAddon>
        </InputGroup>
        <Select
          items={[
            { label: "Every cloud", value: "all" },
            ...providers.map((p) => ({ label: p.name, value: p.id })),
          ]}
          value={cloud}
          onValueChange={(v) => setCloud(v as "all" | ProviderId)}
        >
          <SelectTrigger size="sm" className="w-40" aria-label="Cloud">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Every cloud</SelectItem>
            {providers.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Resource</TableHead>
              <TableHead>Project</TableHead>
              <TableHead>Cloud</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Lifecycle</TableHead>
              <TableHead className="text-right">Monthly</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => {
              const Icon = resourceIcon[r.kind]
              return (
                <TableRow key={r.id}>
                  <TableCell>
                    <div className="flex items-center gap-2.5">
                      <span className="flex size-7 items-center justify-center rounded-md bg-muted">
                        <Icon className="size-3.5 text-muted-foreground" />
                      </span>
                      <span className="grid leading-tight">
                        <span className="font-mono text-[13px] font-medium">
                          {r.name}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          {r.engine}, {r.size}
                        </span>
                      </span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <a
                      href={to(`/projects/${r.project}`)}
                      className={cn(
                        buttonVariants({ variant: "link", size: "xs" }),
                        "h-auto px-0 text-foreground"
                      )}
                    >
                      {r.project}
                    </a>
                  </TableCell>
                  <TableCell>
                    <span className="flex items-center gap-2">
                      <ProviderGlyph id={r.provider} />
                      <span className="text-muted-foreground">{r.region}</span>
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="flex items-center gap-1.5 text-sm">
                      <span
                        className={cn(
                          "size-1.5 rounded-full",
                          r.status === "available" && "bg-overdye",
                          r.status === "provisioning" &&
                            "animate-pulse bg-weld motion-reduce:animate-none",
                          r.status === "unconfirmed" && "ring-1 ring-weld"
                        )}
                      />
                      {r.status === "available"
                        ? "Available"
                        : r.status === "provisioning"
                          ? "Provisioning"
                          : "Needs review"}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Tooltip>
                      <TooltipTrigger render={<span className="inline-flex" />}>
                        <Badge variant="secondary" className="font-normal">
                          {r.managedBy}
                        </Badge>
                      </TooltipTrigger>
                      <TooltipContent className="max-w-xs">
                        {r.managedBy === "Ctrl Plane"
                          ? "Ctrl Plane creates, scales, repairs and removes it."
                          : "Loomplane's planner manages it directly through the cloud's API."}
                      </TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {r.cost ? (
                      money(r.cost, true)
                    ) : (
                      <span className="text-muted-foreground">Not running</span>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
          <TableFooter>
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={5} className="text-muted-foreground">
                {rows.length} {rows.length === 1 ? "resource" : "resources"}
              </TableCell>
              <TableCell className="text-right font-medium tabular-nums">
                {money(total, true)}
              </TableCell>
            </TableRow>
          </TableFooter>
        </Table>
      </Card>
    </section>
  )
}

const connectable: {
  id: string
  name: string
  glyph?: ProviderId
  ready: boolean
}[] = [
  { id: "aws", name: "AWS", glyph: "aws", ready: true },
  { id: "gcp", name: "Google Cloud", glyph: "gcp", ready: true },
  { id: "vercel", name: "Vercel", glyph: "vercel", ready: true },
  { id: "render", name: "Render", glyph: "render", ready: true },
  { id: "azure", name: "Azure", ready: false },
  { id: "fly", name: "Fly.io", ready: false },
]

const setup: Record<string, { how: string; command: string }> = {
  aws: {
    how: "Loomplane assumes a role in your account. Create it with this stack, then paste the role ARN it prints.",
    command:
      "aws cloudformation deploy --template-url https://loomplane.dev/aws/role.yaml --stack-name loomplane --capabilities CAPABILITY_IAM",
  },
  gcp: {
    how: "Loomplane impersonates a service account. Grant it to Loomplane's identity with:",
    command:
      "gcloud iam service-accounts add-iam-policy-binding loomplane@PROJECT.iam.gserviceaccount.com --member=serviceAccount:deployer@loomplane.iam.gserviceaccount.com --role=roles/iam.serviceAccountTokenCreator",
  },
  vercel: {
    how: "Approve Loomplane in Vercel. You pick the team on Vercel's side.",
    command: "loomplane connect vercel",
  },
  render: {
    how: "Create an API key in Render's account settings and paste it here, or run:",
    command: "loomplane connect render --api-key $RENDER_API_KEY",
  },
}

function ConnectDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [choice, setChoice] = useState("aws")
  const chosen = connectable.find((c) => c.id === choice)
  const how = setup[choice]
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect a cloud</DialogTitle>
          <DialogDescription>
            Loomplane never stores long-lived cloud keys when the cloud offers
            something better. Pick a cloud to see how it connects.
          </DialogDescription>
        </DialogHeader>
        <ToggleGroup
          variant="outline"
          value={[choice]}
          onValueChange={(v) => v[0] && setChoice(v[0])}
          className="grid w-full grid-cols-3 items-stretch"
          aria-label="Cloud"
        >
          {connectable.map((c) => (
            <ToggleGroupItem
              key={c.id}
              value={c.id}
              disabled={!c.ready}
              className="h-auto flex-col items-start gap-2 rounded-lg p-3 text-left font-normal aria-pressed:border-foreground/60 aria-pressed:bg-muted/50"
            >
              {c.glyph ? (
                <ProviderGlyph id={c.glyph} className="size-6 text-[10px]" />
              ) : (
                <span className="flex size-6 items-center justify-center rounded-[5px] border text-[10px] font-semibold">
                  {c.name[0]}
                </span>
              )}
              <span className="font-medium">{c.name}</span>
              {!c.ready && (
                <span className="text-xs text-muted-foreground">
                  Coming soon
                </span>
              )}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        {how && (
          <Item variant="muted" className="flex-col items-stretch gap-3">
            <ItemContent>
              <ItemDescription className="line-clamp-none text-foreground">
                {how.how}
              </ItemDescription>
            </ItemContent>
            <InputGroup className="bg-background">
              <InputGroupInput
                readOnly
                value={how.command}
                aria-label="Command"
                className="font-mono text-xs"
              />
              <InputGroupAddon align="inline-end">
                <CopyButton value={how.command} label="Copy command" />
              </InputGroupAddon>
            </InputGroup>
          </Item>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            onClick={() => {
              onOpenChange(false)
              toast(`Waiting for ${chosen?.name ?? "the cloud"} to answer`, {
                description:
                  "Loomplane checks the connection once you've run the command.",
              })
            }}
          >
            I ran it, check now
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
