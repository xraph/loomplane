import { CheckIcon, CopyIcon, UserPlusIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useRemoveMember, useUpdateMember } from "@/client/forge/authsome/hooks"
import {
  useInvitationsCreate,
  useInvitationsList,
  useMembersList,
  useSessionGet,
} from "@/client/forge/loomplane/hooks"
import type {
  CreatedInvitation,
  OrgMember,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Page, PageHeading, SectionHeading } from "@/components/page"
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
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
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
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
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
import { grantable, memberRoleFor, type Role, roleLabel } from "@/lib/roles"

export function Members() {
  const { session, can } = useOrganization()
  const members = useQuery(useMembersList)
  const mayManage = can("member:manage")
  const [inviting, setInviting] = useState(false)
  const me = members.data?.members.find((m) => m.userId === session.userId)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Members" }]} />
      <Page>
        <PageHeading
          title="Members"
          description="Everyone in this organization, and what each of them can do."
          actions={
            mayManage && (
              <Button
                className="rounded-full"
                onClick={() => setInviting(true)}
              >
                <UserPlusIcon />
                Invite
              </Button>
            )
          }
        />

        {members.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(members.error, "Members couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!members.data && members.status !== "error" && (
          <Skeleton className="h-40 w-full rounded-xl" />
        )}
        {members.data && (
          <Card className="py-0">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Person</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead className="hidden md:table-cell">Joined</TableHead>
                  {mayManage && (
                    <TableHead className="w-0">
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.data.members.map((m) => (
                  <MemberRow
                    key={m.id}
                    member={m}
                    isMe={m.userId === session.userId}
                    // Nobody changes their own role or removes themselves here.
                    manageable={mayManage && m.userId !== session.userId}
                    myRole={me?.role ?? "viewer"}
                  />
                ))}
              </TableBody>
            </Table>
          </Card>
        )}

        {mayManage && <PendingInvitations />}
      </Page>

      {mayManage && (
        <InviteDialog
          open={inviting}
          onOpenChange={setInviting}
          myRole={me?.role ?? "viewer"}
        />
      )}
    </>
  )
}

function MemberRow({
  member,
  isMe,
  manageable,
  myRole,
}: {
  member: OrgMember
  isMe: boolean
  manageable: boolean
  myRole: string
}) {
  const { session } = useOrganization()
  const update = useMutation(useUpdateMember)
  const remove = useMutation(useRemoveMember)
  const invalidate = useInvalidate()
  const [confirming, setConfirming] = useState(false)
  const choices = grantable(myRole)
  // authsome's rules: you can't change or remove someone who outranks you.
  const canTouch = manageable && choices.some((r) => r.value === member.role)

  const refresh = () => {
    invalidate(useMembersList)
    invalidate(useSessionGet)
  }

  const changeRole = async (role: Role) => {
    if (role === member.role) return
    try {
      await update.mutateAsync({
        path: { orgId: session.orgId, memberId: member.id },
        body: { role: memberRoleFor(role) },
      })
      toast.success(
        `${member.name || member.email} is now ${roleLabel(role).toLowerCase()}`
      )
    } catch (err) {
      toast.error(messageOf(err, "The role wasn't changed."))
    } finally {
      refresh()
    }
  }

  const removeMember = async () => {
    try {
      await remove.mutateAsync({
        path: { orgId: session.orgId, memberId: member.id },
      })
      toast.success(`Removed ${member.name || member.email}`)
    } catch (err) {
      toast.error(messageOf(err, "The member wasn't removed."))
    } finally {
      // The action is a plain button in Base UI, so closing is ours to do.
      setConfirming(false)
      refresh()
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Avatar className="size-8 rounded-lg">
            <AvatarFallback className="rounded-lg text-xs font-medium">
              {initialsOf(member.name || member.email)}
            </AvatarFallback>
          </Avatar>
          <div className="grid min-w-0">
            <div className="flex items-center gap-2 font-medium">
              <span className="truncate">
                {member.name || member.email || member.userId}
              </span>
              {isMe && (
                <Badge variant="secondary" className="font-normal">
                  You
                </Badge>
              )}
            </div>
            {member.name && (
              <div className="truncate text-xs text-muted-foreground">
                {member.email}
              </div>
            )}
          </div>
        </div>
      </TableCell>
      <TableCell>
        {canTouch ? (
          <Select
            items={choices.map((r) => ({ label: r.label, value: r.value }))}
            value={member.role}
            onValueChange={(v) => void changeRole(v as Role)}
            disabled={update.isPending}
          >
            <SelectTrigger size="sm" aria-label={`Role of ${member.email}`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {choices.map((r) => (
                <SelectItem key={r.value} value={r.value}>
                  {r.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <span className="text-sm">{roleLabel(member.role)}</span>
        )}
      </TableCell>
      <TableCell className="hidden text-muted-foreground md:table-cell">
        <time dateTime={member.joinedAt}>{formatDate(member.joinedAt)}</time>
      </TableCell>
      {manageable && (
        <TableCell>
          {canTouch && (
            <AlertDialog open={confirming} onOpenChange={setConfirming}>
              <AlertDialogTrigger
                render={
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={remove.isPending}
                  />
                }
              >
                Remove
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    Remove {member.name || member.email}?
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    They lose access to this organization's projects at once.
                    You can invite them again later.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Keep them</AlertDialogCancel>
                  <AlertDialogAction
                    variant="destructive"
                    onClick={() => void removeMember()}
                    disabled={remove.isPending}
                  >
                    {remove.isPending && <Spinner />}
                    Remove
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
        </TableCell>
      )}
    </TableRow>
  )
}

function PendingInvitations() {
  const invitations = useQuery(useInvitationsList)
  const list = invitations.data?.invitations ?? []
  if (!invitations.data || list.length === 0) return null
  return (
    <section className="grid gap-3">
      <SectionHeading
        title="Pending invitations"
        description="Each link works once, for a week, for the email it was sent to."
      />
      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Email</TableHead>
              <TableHead>Role</TableHead>
              <TableHead className="hidden md:table-cell">Invited by</TableHead>
              <TableHead className="hidden md:table-cell">Expires</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((inv) => (
              <TableRow key={inv.id}>
                <TableCell className="font-medium">{inv.email}</TableCell>
                <TableCell>{roleLabel(inv.role)}</TableCell>
                <TableCell className="hidden text-muted-foreground md:table-cell">
                  {inv.invitedBy}
                </TableCell>
                <TableCell className="hidden text-muted-foreground md:table-cell">
                  <time dateTime={inv.expiresAt}>
                    {formatDate(inv.expiresAt)}
                  </time>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </section>
  )
}

// Two letters for the avatar: first and last name, or the start of an email.
function initialsOf(nameOrEmail: string): string {
  const words = nameOrEmail.split(/[\s@._-]+/).filter(Boolean)
  return (
    words
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join("") || "?"
  )
}

function InviteDialog({
  open,
  onOpenChange,
  myRole,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  myRole: string
}) {
  const create = useMutation(useInvitationsCreate)
  const choices = grantable(myRole)
  const [email, setEmail] = useState("")
  const [role, setRole] = useState<Role>("developer")
  const [error, setError] = useState("")
  const [link, setLink] = useState("")
  const [copied, setCopied] = useState(false)
  const [emailStatus, setEmailStatus] = useState("")

  const close = (next: boolean) => {
    if (!next) {
      setEmail("")
      setRole("developer")
      setError("")
      setLink("")
      setCopied(false)
      setEmailStatus("")
      create.reset()
    }
    onOpenChange(next)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      // The generator types a mutation's result only when it's an entity,
      // and this one is the invitation plus its link.
      const created = (await create.mutateAsync({
        body: { email: email.trim(), role },
      })) as CreatedInvitation
      setLink(`${window.location.origin}${created.acceptPath}`)
      setEmailStatus(created.emailStatus)
    } catch (err) {
      setError(messageOf(err, "The invitation wasn't created. Try again."))
    }
  }

  const copy = async () => {
    await navigator.clipboard.writeText(link)
    setCopied(true)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-md">
        {link ? (
          <>
            <DialogHeader>
              <DialogTitle>
                {emailStatus === "sent"
                  ? "Invitation sent"
                  : "Share the invitation link"}
              </DialogTitle>
              <DialogDescription>
                {emailStatus === "sent"
                  ? `An invitation email was sent to ${email}. You can also share this link.`
                  : emailStatus === "failed"
                    ? `Email delivery failed. Share this link with ${email}, or check the mail provider.`
                    : `Email is disabled. Share this link with ${email}.`}{" "}
                It works once, for a week. They must sign in with that address.
              </DialogDescription>
            </DialogHeader>
            <div className="flex gap-2">
              <Input
                readOnly
                value={link}
                aria-label="Invitation link"
                onFocus={(e) => e.currentTarget.select()}
              />
              <Button
                variant="outline"
                onClick={() => void copy()}
                aria-label="Copy link"
              >
                {copied ? <CheckIcon /> : <CopyIcon />}
                {copied ? "Copied" : "Copy"}
              </Button>
            </div>
            <DialogFooter>
              <Button onClick={() => close(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit}>
            <DialogHeader>
              <DialogTitle>Invite someone</DialogTitle>
              <DialogDescription>
                The invitation is emailed when mail is configured. You also get
                a link to share. They sign in with this address, and accept.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-4">
              <Field>
                <FieldLabel htmlFor="invite-email">Email</FieldLabel>
                <Input
                  id="invite-email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="grace@example.com"
                  autoFocus
                  required
                />
              </Field>
              <Field>
                <FieldLabel>Role</FieldLabel>
                <Select
                  items={choices.map((r) => ({
                    label: r.label,
                    value: r.value,
                  }))}
                  value={role}
                  onValueChange={(v) => setRole(v as Role)}
                >
                  <SelectTrigger aria-label="Role" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {choices.map((r) => (
                      <SelectItem key={r.value} value={r.value}>
                        {r.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FieldDescription>
                  {choices.find((r) => r.value === role)?.description}
                </FieldDescription>
              </Field>
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </FieldGroup>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => close(false)}
                disabled={create.isPending}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={create.isPending || !email.trim()}
              >
                {create.isPending && <Spinner />}
                Create invitation
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
