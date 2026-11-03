import { PlusIcon, Trash2Icon, UsersRoundIcon, XIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useMembersList,
  useProjectsList,
  useTeamsAddMember,
  useTeamsCreate,
  useTeamsDelete,
  useTeamsList,
  useTeamsRemoveGrant,
  useTeamsRemoveMember,
  useTeamsSetGrant,
  useTeamsUpdate,
} from "@/client/forge/loomplane/hooks"
import type { Team } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
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
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { useOrganization } from "@/lib/organization"
import { grantable, roleLabel } from "@/lib/roles"
import { grantLabel } from "@/lib/teams"

export function Teams() {
  const { can } = useOrganization()
  const teams = useQuery(useTeamsList)
  const projects = useQuery(useProjectsList)
  const mayManage = can("member:manage")
  const [creating, setCreating] = useState(false)
  const projectList = projects.data?.projects ?? []
  const projectName = (id: string) =>
    projectList.find((p) => p.id === id)?.name ?? id

  return (
    <>
      <SiteHeader crumbs={[{ label: "Teams" }]} />
      <Page className="max-w-[980px]">
        <PageHeading
          title="Teams"
          description="Groups of people who share a role on a project or on the whole workspace. A team's role is copied onto each member, and a person keeps the highest of their own role and their teams'."
          actions={
            mayManage && (
              <Button onClick={() => setCreating(true)}>
                <PlusIcon />
                Create team
              </Button>
            )
          }
        />

        {teams.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(teams.error, "Teams couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!teams.data && teams.status !== "error" && (
          <Skeleton className="h-40 w-full rounded-xl" />
        )}
        {teams.data && (teams.data.teams ?? []).length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <UsersRoundIcon />
              </EmptyMedia>
              <EmptyTitle>No teams yet</EmptyTitle>
              <EmptyDescription>
                A team gives a group of people a role on one project, so the
                right people can deploy it without being admins everywhere.
              </EmptyDescription>
            </EmptyHeader>
            {mayManage && (
              <EmptyContent>
                <Button onClick={() => setCreating(true)}>
                  <PlusIcon />
                  Create team
                </Button>
              </EmptyContent>
            )}
          </Empty>
        )}
        {teams.data?.teams?.map((team) => (
          <TeamCard
            key={team.id}
            team={team}
            mayManage={mayManage}
            projectName={projectName}
            projects={projectList.map((p) => ({ id: p.id, name: p.name }))}
          />
        ))}
      </Page>

      {mayManage && <TeamDialog open={creating} onOpenChange={setCreating} />}
    </>
  )
}

function TeamCard({
  team,
  mayManage,
  projectName,
  projects,
}: {
  team: Team
  mayManage: boolean
  projectName: (id: string) => string
  projects: { id: string; name: string }[]
}) {
  const remove = useMutation(useTeamsDelete)
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)

  const destroy = async () => {
    try {
      await remove.mutateAsync({ path: { id: team.id } })
      toast.success(`Deleted ${team.name}`)
    } catch (err) {
      toast.error(messageOf(err, "The team wasn't deleted."))
    } finally {
      setConfirming(false)
    }
  }

  return (
    <Card className="gap-0 py-0" aria-label={team.name} role="region">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3">
        <div className="grid min-w-0 gap-0.5">
          <h2 className="flex items-center gap-2 text-[14px] font-semibold">
            <UsersRoundIcon className="size-4 text-muted-foreground" />
            <span className="truncate">{team.name}</span>
            <code className="font-mono text-xs font-normal text-muted-foreground">
              {team.slug}
            </code>
          </h2>
          {team.description && (
            <p className="max-w-[72ch] text-[12.5px] text-muted-foreground">
              {team.description}
            </p>
          )}
        </div>
        {mayManage && (
          <div className="flex items-center gap-1.5">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setEditing(true)}
            >
              Edit
            </Button>
            <AlertDialog open={confirming} onOpenChange={setConfirming}>
              <AlertDialogTrigger
                render={
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={remove.isPending}
                  />
                }
              >
                <Trash2Icon />
                Delete
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Delete {team.name}?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Its members lose the roles it gave them. Projects that named
                    it go back to sending requests to workspace admins.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Keep team</AlertDialogCancel>
                  <AlertDialogAction
                    variant="destructive"
                    onClick={() => void destroy()}
                    disabled={remove.isPending}
                  >
                    {remove.isPending && <Spinner />}
                    Delete team
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        )}
      </div>

      <div className="grid divide-y md:grid-cols-2 md:divide-x md:divide-y-0">
        <MembersPane team={team} mayManage={mayManage} />
        <GrantsPane
          team={team}
          mayManage={mayManage}
          projectName={projectName}
          projects={projects}
        />
      </div>

      {mayManage && (
        <TeamDialog
          key={team.updatedAt}
          team={team}
          open={editing}
          onOpenChange={setEditing}
        />
      )}
    </Card>
  )
}

function MembersPane({ team, mayManage }: { team: Team; mayManage: boolean }) {
  const remove = useMutation(useTeamsRemoveMember)

  const take = async (userId: string, who: string) => {
    try {
      await remove.mutateAsync({ path: { id: team.id, user_id: userId } })
      toast.success(`Removed ${who} from ${team.name}`)
    } catch (err) {
      toast.error(messageOf(err, "The person wasn't removed."))
    }
  }

  return (
    <section
      aria-label={`Members of ${team.name}`}
      className="grid content-start gap-2 px-4 py-3"
    >
      <h3 className="text-[13px] font-semibold">Members</h3>
      {team.members.length === 0 ? (
        <p className="text-[12.5px] text-muted-foreground">Nobody yet.</p>
      ) : (
        <ul className="grid gap-1">
          {team.members.map((m) => {
            const who = m.name || m.email || m.userId
            return (
              <li
                key={m.userId}
                className="flex items-center justify-between gap-2"
              >
                <div className="grid min-w-0">
                  <span className="truncate text-[13px] font-medium">
                    {who}
                  </span>
                  {m.name && (
                    <span className="truncate text-xs text-muted-foreground">
                      {m.email}
                    </span>
                  )}
                </div>
                {mayManage && (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Remove ${who} from ${team.name}`}
                    disabled={remove.isPending}
                    onClick={() => void take(m.userId, who)}
                  >
                    <XIcon />
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {mayManage && <AddMember team={team} />}
    </section>
  )
}

function AddMember({ team }: { team: Team }) {
  const members = useQuery(useMembersList)
  const add = useMutation(useTeamsAddMember)
  const inTeam = new Set(team.members.map((m) => m.userId))
  const choices = (members.data?.members ?? []).filter(
    (m) => !inTeam.has(m.userId)
  )

  const put = async (userId: string | null) => {
    const who = choices.find((m) => m.userId === userId)
    if (!userId || !who) return
    try {
      await add.mutateAsync({ path: { id: team.id, user_id: userId } })
      toast.success(`Added ${who.name || who.email} to ${team.name}`)
    } catch (err) {
      toast.error(messageOf(err, "The person wasn't added."))
    }
  }

  return (
    <Select
      items={choices.map((m) => ({
        label: m.name || m.email,
        value: m.userId,
      }))}
      value={null}
      onValueChange={(v) => void put(v)}
      disabled={add.isPending || choices.length === 0}
    >
      <SelectTrigger
        size="sm"
        className="w-full max-w-[260px]"
        aria-label={`Add a member to ${team.name}`}
      >
        <SelectValue
          placeholder={
            members.data && choices.length === 0
              ? "Everyone is in this team"
              : "Add a member"
          }
        />
      </SelectTrigger>
      <SelectContent>
        {choices.map((m) => (
          <SelectItem key={m.userId} value={m.userId}>
            {m.name || m.email}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

const workspace = "workspace"

function GrantsPane({
  team,
  mayManage,
  projectName,
  projects,
}: {
  team: Team
  mayManage: boolean
  projectName: (id: string) => string
  projects: { id: string; name: string }[]
}) {
  const remove = useMutation(useTeamsRemoveGrant)

  const revoke = async (grantId: string) => {
    try {
      await remove.mutateAsync({ path: { id: team.id, grant_id: grantId } })
      toast.success("Removed the role")
    } catch (err) {
      toast.error(messageOf(err, "The role wasn't removed."))
    }
  }

  return (
    <section
      aria-label={`Roles of ${team.name}`}
      className="grid content-start gap-2 px-4 py-3"
    >
      <h3 className="text-[13px] font-semibold">Roles</h3>
      {team.grants.length === 0 ? (
        <p className="text-[12.5px] text-muted-foreground">
          No role yet, so the team gives its members nothing.
        </p>
      ) : (
        <ul className="grid gap-1">
          {team.grants.map((g) => {
            const label = grantLabel(g, projectName)
            return (
              <li
                key={g.id}
                className="flex items-center justify-between gap-2"
              >
                <Badge variant="secondary" className="max-w-full text-[12.5px]">
                  <span className="truncate">{label}</span>
                </Badge>
                {mayManage && (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Remove ${label}`}
                    disabled={remove.isPending}
                    onClick={() => void revoke(g.id)}
                  >
                    <XIcon />
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {mayManage && <GrantEditor team={team} projects={projects} />}
    </section>
  )
}

// What a team can give: no owner, and nothing above the caller's own role.
const teamRoles = (own: string) =>
  grantable(own).filter((r) => r.value !== "owner")

function GrantEditor({
  team,
  projects,
}: {
  team: Team
  projects: { id: string; name: string }[]
}) {
  const { session } = useOrganization()
  const members = useQuery(useMembersList)
  const set = useMutation(useTeamsSetGrant)
  const [role, setRole] = useState("developer")
  const [scope, setScope] = useState(workspace)
  const me = members.data?.members.find((m) => m.userId === session.userId)
  const roles = teamRoles(me?.role ?? "admin")
  const scopes = [
    { label: "Whole workspace", value: workspace },
    ...projects.map((p) => ({ label: p.name, value: p.id })),
  ]

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await set.mutateAsync({
        path: { id: team.id },
        body: scope === workspace ? { role } : { role, projectId: scope },
      })
      toast.success(`${team.name} is now ${roleLabel(role).toLowerCase()}`)
    } catch (err) {
      toast.error(messageOf(err, "The role wasn't set."))
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="flex flex-wrap items-center gap-2"
    >
      <Select
        items={roles.map((r) => ({ label: r.label, value: r.value }))}
        value={role}
        onValueChange={(v) => v && setRole(v)}
      >
        <SelectTrigger size="sm" aria-label={`Role for ${team.name}`}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {roles.map((r) => (
            <SelectItem key={r.value} value={r.value}>
              {r.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        items={scopes}
        value={scope}
        onValueChange={(v) => v && setScope(v)}
      >
        <SelectTrigger size="sm" aria-label={`Scope for ${team.name}`}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {scopes.map((s) => (
            <SelectItem key={s.value} value={s.value}>
              {s.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        type="submit"
        size="sm"
        variant="outline"
        disabled={set.isPending}
      >
        {set.isPending && <Spinner />}
        Set role
      </Button>
    </form>
  )
}

function TeamDialog({
  team,
  open,
  onOpenChange,
}: {
  team?: Team
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const create = useMutation(useTeamsCreate)
  const update = useMutation(useTeamsUpdate)
  const [name, setName] = useState(team?.name ?? "")
  const [description, setDescription] = useState(team?.description ?? "")
  const [error, setError] = useState("")
  const pending = create.isPending || update.isPending

  const close = (next: boolean) => {
    if (!next) {
      setError("")
      if (!team) {
        setName("")
        setDescription("")
      }
    }
    onOpenChange(next)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    const trimmed = name.trim()
    const about = description.trim()
    try {
      if (team) {
        await update.mutateAsync({
          path: { id: team.id },
          body: { name: trimmed, description: about },
        })
      } else {
        await create.mutateAsync({
          body: about
            ? { name: trimmed, description: about }
            : { name: trimmed },
        })
      }
      close(false)
    } catch (err) {
      setError(messageOf(err, "The team wasn't saved. Try again."))
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-[480px]">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>
              {team ? `Edit ${team.name}` : "Create a team"}
            </DialogTitle>
            <DialogDescription>
              The agent reads the description to pick the team a request should
              go to, so say what the team looks after.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`team-name-${team?.id ?? "new"}`}>
                Name
              </FieldLabel>
              <Input
                id={`team-name-${team?.id ?? "new"}`}
                value={name}
                maxLength={80}
                required
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={`team-about-${team?.id ?? "new"}`}>
                Description
              </FieldLabel>
              <Textarea
                id={`team-about-${team?.id ?? "new"}`}
                value={description}
                maxLength={500}
                rows={3}
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>
          </FieldGroup>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => close(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={pending || name.trim() === ""}>
              {pending && <Spinner />}
              {team ? "Save" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
