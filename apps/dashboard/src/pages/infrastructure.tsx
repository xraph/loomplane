import { CloudIcon, CpuIcon, DatabaseIcon } from "lucide-react"
import type { ReactNode } from "react"

import { messageOf } from "@/client/errors"
import { useInfrastructureGet } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

export function Infrastructure() {
  const infra = useQuery(useInfrastructureGet)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Infrastructure" }]} />
      <Page>
        <PageHeading
          title="Infrastructure"
          description="Where Loomplane can deploy. Ctrl Plane keeps the lifecycle of every instance and release it manages."
        />

        {infra.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(infra.error, "Infrastructure couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!infra.data && infra.status !== "error" && (
          <div className="grid gap-4 md:grid-cols-2">
            <Skeleton className="h-32 rounded-xl" />
            <Skeleton className="h-32 rounded-xl" />
          </div>
        )}

        {infra.data && (
          <>
            <div className="grid gap-4 md:grid-cols-2">
              <Fact
                icon={<CpuIcon />}
                title="Lifecycle engine"
                value={infra.data.engine}
                note="Starts, stops and replaces what Loomplane deploys, and remembers what it did."
              />
              <Fact
                icon={<DatabaseIcon />}
                title="Engine state"
                value={infra.data.store}
                note="Where the engine records every instance and release."
              />
            </div>

            <section className="grid gap-3">
              <SectionHeading
                title="Clouds"
                description="The providers Loomplane can place services on, and what each can run."
              />
              {infra.data.providers.length === 0 ? (
                <Empty className="border border-dashed">
                  <EmptyHeader>
                    <EmptyMedia variant="icon">
                      <CloudIcon />
                    </EmptyMedia>
                    <EmptyTitle>No clouds connected yet</EmptyTitle>
                    <EmptyDescription>
                      Vercel, Google Cloud Run, AWS ECS and Render arrive with
                      deployments, as Loomplane adapters registered with Ctrl
                      Plane.
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              ) : (
                <Card className="py-0">
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>Provider</TableHead>
                        <TableHead>Region</TableHead>
                        <TableHead>Can run</TableHead>
                        <TableHead className="hidden md:table-cell">
                          Version
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {infra.data.providers.map((p) => (
                        <TableRow key={p.name}>
                          <TableCell className="font-medium">
                            {p.name}
                          </TableCell>
                          <TableCell>{p.region || "Any"}</TableCell>
                          <TableCell>
                            <div className="flex flex-wrap gap-1">
                              {p.capabilities.map((c) => (
                                <Badge
                                  key={c}
                                  variant="outline"
                                  className="font-normal"
                                >
                                  {c}
                                </Badge>
                              ))}
                            </div>
                          </TableCell>
                          <TableCell className="hidden font-mono text-xs text-muted-foreground md:table-cell">
                            {p.version}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </Card>
              )}
            </section>
          </>
        )}
      </Page>
    </>
  )
}

function Fact({
  icon,
  title,
  value,
  note,
}: {
  icon: ReactNode
  title: string
  value: string
  note: string
}) {
  return (
    <Card>
      <CardHeader>
        <CardDescription className="flex items-center gap-2 [&_svg]:size-4">
          {icon}
          {title}
        </CardDescription>
        <CardTitle className="text-2xl font-semibold tracking-tight">
          {value}
        </CardTitle>
      </CardHeader>
      <CardContent className="text-sm text-muted-foreground">
        {note}
      </CardContent>
    </Card>
  )
}
