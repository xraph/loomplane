import { PlusIcon, SearchIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useVariablesCreate,
  useVariablesList,
  useVariablesResolve,
  useVariablesUpdate,
} from "@/client/forge/loomplane/hooks"
import type { Variable } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { NoMatchIcon, VariablesIcon } from "@/components/icons"
import { DeleteVariable, Value } from "@/components/project-variables"
import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  type VariableInput,
  VariableDialog,
} from "@/components/variable-dialog"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"

/**
 * Variables is the organization-wide layer: values every project inherits,
 * in every environment or in each project's environment of one name.
 */
export function Variables() {
  const { can } = useOrganization()
  const list = useQuery(useVariablesList)
  const mayManage = can("organization_variable:manage")
  const [dialog, setDialog] = useState<
    { kind: "create" } | { kind: "edit"; variable: Variable } | null
  >(null)
  const create = useMutation(useVariablesCreate)
  const update = useMutation(useVariablesUpdate)
  const invalidate = useInvalidate()
  const [query, setQuery] = useState("")
  const q = query.trim().toLowerCase()
  const shown = list.data?.variables.filter(
    (v) => !q || v.key.toLowerCase().includes(q)
  )

  const save = async (input: VariableInput) => {
    if (dialog?.kind === "edit") {
      await update.mutateAsync({
        path: { id: dialog.variable.id },
        body: { value: input.value, secret: input.secret },
      })
      toast.success(`Saved ${dialog.variable.key}`)
    } else {
      await create.mutateAsync({
        body: {
          key: input.key,
          value: input.value,
          secret: input.secret,
          environment: input.environment || undefined,
        },
      })
      toast.success(`Set ${input.key}`)
    }
    invalidate(useVariablesList)
    invalidate(useVariablesResolve)
  }

  return (
    <>
      <SiteHeader crumbs={[{ label: "Variables" }]} />
      <Page className="max-w-[980px]">
        <PageHeading
          title="Variables"
          description="Organization-wide values every project inherits. A project, or one of its services, can override any of them on its own page."
          actions={
            mayManage && (
              <Button onClick={() => setDialog({ kind: "create" })}>
                <PlusIcon />
                Set variable
              </Button>
            )
          }
        />

        {list.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(list.error, "Variables couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!list.data && list.status !== "error" && (
          <Skeleton className="h-40 w-full rounded-xl" />
        )}
        {list.data && list.data.variables.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <VariablesIcon />
              </EmptyMedia>
              <EmptyTitle>No organization-wide variables yet</EmptyTitle>
              <EmptyDescription>
                {mayManage
                  ? "Set one here to give every project the same value, such as a region or a log level."
                  : "Owners and admins set them."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {list.data && list.data.variables.length > 0 && (
          <InputGroup className="h-9 rounded-lg border-transparent bg-muted">
            <InputGroupAddon>
              <SearchIcon />
            </InputGroupAddon>
            <InputGroupInput
              data-page-search
              placeholder="Filter by key"
              aria-label="Filter variables"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </InputGroup>
        )}
        {shown && list.data && list.data.variables.length > 0 && (
          <Card className="py-0">
            {shown.length === 0 ? (
              <Empty surface={false} className="py-8">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <NoMatchIcon />
                  </EmptyMedia>
                  <EmptyTitle>No variables match</EmptyTitle>
                  <EmptyDescription>
                    Try another key, or set a new one for every project.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>Key</TableHead>
                    <TableHead>Value</TableHead>
                    <TableHead>Applies in</TableHead>
                    <TableHead className="hidden md:table-cell">
                      Changed
                    </TableHead>
                    {mayManage && (
                      <TableHead className="w-0">
                        <span className="sr-only">Actions</span>
                      </TableHead>
                    )}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.map((v) => (
                    <TableRow key={v.id}>
                      <TableCell className="font-mono text-[12.5px] font-medium">
                        {v.key}
                      </TableCell>
                      <TableCell className="max-w-xs">
                        <Value variable={v} />
                      </TableCell>
                      <TableCell className="text-[12.5px]">
                        {v.environment ? (
                          <span>
                            every project&apos;s{" "}
                            <span className="font-medium">{v.environment}</span>
                          </span>
                        ) : (
                          "every environment"
                        )}
                      </TableCell>
                      <TableCell className="hidden text-[12.5px] text-muted-foreground md:table-cell">
                        <time dateTime={v.updatedAt}>
                          {formatDate(v.updatedAt)}
                        </time>
                      </TableCell>
                      {mayManage && (
                        <TableCell>
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="xs"
                              onClick={() =>
                                setDialog({ kind: "edit", variable: v })
                              }
                            >
                              Edit
                            </Button>
                            <DeleteVariable variable={v} />
                          </div>
                        </TableCell>
                      )}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </Card>
        )}
        {list.data && list.data.variables.length > 0 && (
          <p className="text-[12.5px] leading-normal text-muted-foreground">
            A project&apos;s own value always wins over the one here. Secrets
            are encrypted and never shown again, so editing one means typing a
            new value.
          </p>
        )}
      </Page>
      <VariableDialog
        open={dialog !== null}
        onOpenChange={(open) => !open && setDialog(null)}
        title={
          dialog?.kind === "edit"
            ? `Change ${dialog.variable.key}`
            : "Set an organization-wide variable"
        }
        description={
          dialog?.kind === "edit"
            ? "Every project that doesn't override it gets the new value."
            : "Every project inherits it, unless the project or one of its services sets the key itself."
        }
        environments={{ mode: "organization" }}
        editing={dialog?.kind === "edit" ? dialog.variable : undefined}
        submit={save}
      />
    </>
  )
}
