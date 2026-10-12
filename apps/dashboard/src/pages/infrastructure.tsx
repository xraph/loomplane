import { CloudIcon } from "lucide-react"

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
import { messageOf } from "@/client/errors"
import { useInfrastructureGet } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"

export function Infrastructure() {
  const infra = useQuery(useInfrastructureGet)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Infrastructure" }]} />
      <div className="flex flex-1 flex-col gap-6 p-4 md:p-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Infrastructure
          </h1>
          <p className="max-w-prose text-sm text-muted-foreground">
            Where Loomplane can deploy. Ctrl Plane keeps the lifecycle of every
            instance and release it manages.
          </p>
        </div>

        {infra.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(infra.error, "Infrastructure couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!infra.data && infra.status !== "error" && (
          <Skeleton className="h-48 w-full" />
        )}

        {infra.data && (
          <Card>
            <CardHeader>
              <CardTitle>Deployment providers</CardTitle>
              <CardDescription>
                Lifecycle engine{" "}
                <Badge variant="secondary">{infra.data.engine}</Badge> with its
                state in <Badge variant="secondary">{infra.data.store}</Badge>
              </CardDescription>
            </CardHeader>
            <CardContent>
              {infra.data.providers.length === 0 ? (
                <Empty className="border">
                  <EmptyHeader>
                    <EmptyMedia variant="icon">
                      <CloudIcon />
                    </EmptyMedia>
                    <EmptyTitle>No providers connected yet</EmptyTitle>
                    <EmptyDescription>
                      Vercel, Google Cloud Run, AWS ECS and Render arrive with
                      deployments, as Loomplane adapters registered with Ctrl
                      Plane.
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              ) : (
                <div className="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader className="bg-muted">
                      <TableRow>
                        <TableHead>Provider</TableHead>
                        <TableHead>Region</TableHead>
                        <TableHead>Can</TableHead>
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
                                <Badge key={c} variant="outline">
                                  {c}
                                </Badge>
                              ))}
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </CardContent>
          </Card>
        )}
      </div>
    </>
  )
}
