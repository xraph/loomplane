import { ExternalLinkIcon, LockIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useGithubDisconnect,
  useGithubRepositories,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import type { GitHubInstallation } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudConnections } from "@/components/cloud-connections"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

export function Integrations() {
  const { can } = useOrganization()
  const status = useQuery(useGithubStatus)
  const mayConnect = can("repository:connect")

  return (
    <>
      <SiteHeader crumbs={[{ label: "Integrations" }]} />
      <Page>
        <PageHeading
          title="Integrations"
          description="Where Loomplane reads your code from, and the clouds it deploys to."
        />

        <Card>
          <CardHeader>
            <CardTitle>GitHub</CardTitle>
            <CardDescription>
              Public repositories need nothing here. Connect the GitHub App to
              let Loomplane read private ones: it gets a read-only token for one
              repository at a time, for as long as an analysis takes, and keeps
              none.
            </CardDescription>
            {status.data?.installUrl && (
              <CardAction>
                {/* A link, not a button: it leaves for GitHub. */}
                <a
                  href={status.data.installUrl}
                  className={cn(buttonVariants())}
                >
                  {status.data.installations.length > 0
                    ? "Connect another account"
                    : "Connect GitHub"}
                </a>
              </CardAction>
            )}
          </CardHeader>
          <CardContent className="flex flex-col gap-6">
            {status.status === "error" && (
              <Alert variant="destructive">
                <AlertDescription>
                  {messageOf(
                    status.error,
                    "The GitHub connection couldn't be loaded."
                  )}
                </AlertDescription>
              </Alert>
            )}
            {!status.data && status.status !== "error" && (
              <Skeleton className="h-24 w-full" />
            )}
            {status.data && !status.data.configured && (
              <p className="text-sm text-muted-foreground">
                This Loomplane has no GitHub App set up, so it reads public
                repositories only. Whoever runs it can add one; the steps are in
                docs/github-app.md.
              </p>
            )}
            {status.data?.configured &&
              status.data.installations.length === 0 && (
                <p className="text-sm text-muted-foreground">
                  {mayConnect
                    ? "No GitHub accounts connected yet. Connect one, then pick which of its repositories Loomplane may read."
                    : "No GitHub accounts connected yet. An admin can connect one."}
                </p>
              )}
            {status.data && status.data.installations.length > 0 && (
              <>
                <Card className="py-0">
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>Account</TableHead>
                        <TableHead className="hidden sm:table-cell">
                          Connected
                        </TableHead>
                        {mayConnect && (
                          <TableHead className="w-0">
                            <span className="sr-only">Actions</span>
                          </TableHead>
                        )}
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {status.data.installations.map((inst) => (
                        <TableRow key={inst.id}>
                          <TableCell>
                            <span className="font-medium">{inst.account}</span>{" "}
                            <span className="text-muted-foreground">
                              {inst.accountType === "User"
                                ? "personal account"
                                : "organization"}
                            </span>
                          </TableCell>
                          <TableCell className="hidden text-muted-foreground sm:table-cell">
                            <time dateTime={inst.createdAt}>
                              {formatDate(inst.createdAt)}
                            </time>
                          </TableCell>
                          {mayConnect && (
                            <TableCell>
                              <Disconnect installation={inst} />
                            </TableCell>
                          )}
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </Card>
                <Repositories />
              </>
            )}
          </CardContent>
        </Card>

        <CloudConnections />
      </Page>
    </>
  )
}

function Repositories() {
  const repos = useQuery(useGithubRepositories)
  if (repos.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(repos.error, "The repositories couldn't be listed.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!repos.data) return <Skeleton className="h-16 w-full" />
  const list = repos.data.repositories
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">
        Repositories Loomplane can read ({list.length})
      </h3>
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          None. Choose repositories in the App&apos;s settings on GitHub.
        </p>
      ) : (
        <ul className="flex flex-col gap-1 text-sm">
          {list.map((r) => (
            <li key={r.fullName} className="flex items-center gap-2">
              <a
                href={r.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 font-mono underline-offset-4 hover:underline"
              >
                {r.fullName}
                <ExternalLinkIcon className="size-3" aria-hidden="true" />
              </a>
              {r.private && (
                <Badge variant="outline" className="gap-1">
                  <LockIcon aria-hidden="true" />
                  Private
                </Badge>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function Disconnect({ installation }: { installation: GitHubInstallation }) {
  const remove = useMutation(useGithubDisconnect)
  const invalidate = useInvalidate()
  // Controlled: Base UI's AlertDialogAction doesn't close the dialog.
  const [open, setOpen] = useState(false)

  const confirm = async () => {
    try {
      await remove.mutateAsync({ path: { id: installation.id } })
      toast.success(`Disconnected ${installation.account}`)
    } catch (err) {
      toast.error(messageOf(err, "It wasn't disconnected."))
    } finally {
      setOpen(false)
      invalidate(useGithubStatus)
      invalidate(useGithubRepositories)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button variant="ghost" size="sm" disabled={remove.isPending} />
        }
      >
        Disconnect
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Disconnect {installation.account}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Loomplane stops reading its private repositories. Analyses already
            done stay. The App stays installed on GitHub; uninstall it there if
            you want it gone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => void confirm()}
          >
            {remove.isPending && <Spinner />}
            Disconnect
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
