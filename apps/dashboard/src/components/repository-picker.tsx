// A repository field: type or paste any GitHub repository, or pick one the
// organization's connected GitHub accounts let Loomplane read, from a list
// grouped by account. New project and Add a repository both use it.
import { Autocomplete } from "@base-ui/react/autocomplete"
import { GitBranchIcon, LockIcon } from "lucide-react"
import type { ReactNode } from "react"

import {
  useGithubRepositories,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import type {
  ConnectedRepository,
  GitHubInstallation,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { githubRepo } from "@/lib/format"
import { type RepositoryChoice, repositoryChoices } from "@/lib/integrations"
import { cn } from "@/lib/utils"

type FieldProps = {
  id?: string
  /** owner/name, or whatever's been typed so far. */
  value: string
  onChange: (value: string) => void
  /** Repositories already chosen elsewhere in the form, as owner/name. */
  exclude?: string[]
  placeholder?: string
  autoFocus?: boolean
  required?: boolean
  "aria-label"?: string
  onFocus?: () => void
  /** Controls at the field's end, such as a remove button. */
  end?: ReactNode
  className?: string
}

/** A pasted URL becomes owner/name, which is what the field holds. */
function normalize(typed: string): string {
  return githubRepo(typed) || typed.trim()
}

export function RepositoryPicker(props: FieldProps) {
  const github = useQuery(useGithubStatus)
  const installations = github.data?.installations ?? []
  // The list asks GitHub, so it's only there once an account is connected.
  if (installations.length === 0) return <PlainField {...props} />
  return <Picker {...props} installations={installations} />
}

function PlainField({
  id,
  value,
  onChange,
  placeholder,
  autoFocus,
  required,
  onFocus,
  end,
  className,
  ...rest
}: FieldProps) {
  return (
    <InputGroup className={className}>
      <InputGroupAddon>
        <InputGroupText>github.com/</InputGroupText>
      </InputGroupAddon>
      <InputGroupInput
        id={id}
        autoFocus={autoFocus}
        required={required}
        placeholder={placeholder}
        aria-label={rest["aria-label"]}
        value={value}
        onFocus={onFocus}
        onChange={(e) => onChange(normalize(e.target.value))}
        className="font-mono text-[12.5px]"
        autoComplete="off"
        spellCheck={false}
      />
      {end}
    </InputGroup>
  )
}

function Picker({
  id,
  value,
  onChange,
  exclude = [],
  placeholder,
  autoFocus,
  required,
  onFocus,
  end,
  className,
  installations,
  ...rest
}: FieldProps & { installations: GitHubInstallation[] }) {
  const repos = useQuery(useGithubRepositories)
  const all = repos.data?.repositories ?? []
  const groups = repositoryChoices(all, installations, value, exclude)

  return (
    <div className="grid min-w-0 gap-2">
      <Autocomplete.Root
        items={groups}
        filteredItems={groups}
        value={value}
        onValueChange={(v) => onChange(normalize(v))}
        itemToStringValue={(c: RepositoryChoice) => c.repository.fullName}
        openOnInputClick
      >
        <Autocomplete.InputGroup render={<InputGroup className={className} />}>
          <InputGroupAddon>
            <InputGroupText>github.com/</InputGroupText>
          </InputGroupAddon>
          <Autocomplete.Input
            id={id}
            autoFocus={autoFocus}
            required={required}
            placeholder={placeholder}
            aria-label={rest["aria-label"]}
            onFocus={onFocus}
            render={<InputGroupInput />}
            className="font-mono text-[12.5px]"
            autoComplete="off"
            spellCheck={false}
          />
          {!repos.data && repos.status !== "error" && (
            <InputGroupAddon align="inline-end">
              <Spinner className="text-muted-foreground" />
            </InputGroupAddon>
          )}
          {end}
        </Autocomplete.InputGroup>
        {repos.data && all.length > 0 && (
          <Autocomplete.Portal>
            <Autocomplete.Positioner sideOffset={6} className="isolate z-50">
              <Autocomplete.Popup className="max-h-[min(22rem,var(--available-height))] w-(--anchor-width) origin-(--transform-origin) overflow-y-auto overscroll-contain rounded-xl bg-popover p-1 text-[13px] text-popover-foreground shadow-md ring-1 ring-foreground/10 duration-100 data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0">
                <Autocomplete.Empty className="px-3 py-4 text-center text-xs text-muted-foreground empty:hidden">
                  No connected repository matches. Any public one works too.
                </Autocomplete.Empty>
                <Autocomplete.List>
                  {(group: (typeof groups)[number]) => (
                    <Autocomplete.Group
                      key={group.key}
                      items={group.items}
                      className="pb-1"
                    >
                      <Autocomplete.GroupLabel className="flex items-center gap-2 px-2 pt-2 pb-1 text-xs font-medium text-muted-foreground">
                        {group.avatarUrl ? (
                          <img
                            src={group.avatarUrl}
                            alt=""
                            className="size-4 rounded-[4px] border"
                          />
                        ) : (
                          <span className="size-4 rounded-[4px] border bg-muted" />
                        )}
                        {group.account}
                      </Autocomplete.GroupLabel>
                      <Autocomplete.Collection>
                        {(c: RepositoryChoice) => (
                          <Choice key={c.repository.fullName} choice={c} />
                        )}
                      </Autocomplete.Collection>
                    </Autocomplete.Group>
                  )}
                </Autocomplete.List>
              </Autocomplete.Popup>
            </Autocomplete.Positioner>
          </Autocomplete.Portal>
        )}
      </Autocomplete.Root>
      {repos.status === "error" && (
        <QueryError
          error={repos.error}
          fallback="The repositories couldn't be loaded. You can still type a repository."
          retry={repos.refetch}
          label="Retry repositories"
        />
      )}
    </div>
  )
}

function Choice({
  choice: { repository: r, added },
}: {
  choice: RepositoryChoice
}) {
  return (
    <Autocomplete.Item
      value={{ repository: r, added }}
      disabled={added}
      className={cn(
        "flex cursor-default items-center gap-2 rounded-lg px-2 py-1.5 outline-none select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground",
        added && "opacity-50"
      )}
    >
      <span className="min-w-0 flex-1 truncate font-mono text-[13px]">
        {name(r)}
      </span>
      {r.private && (
        <LockIcon
          className="size-3 shrink-0 text-muted-foreground"
          aria-label="Private"
        />
      )}
      <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
        {added ? (
          "Added"
        ) : (
          <>
            <GitBranchIcon className="size-3" aria-hidden="true" />
            {r.defaultBranch || "main"}
          </>
        )}
      </span>
    </Autocomplete.Item>
  )
}

/** name is a repository's name without its owner, which the group shows. */
function name(r: ConnectedRepository): string {
  return r.fullName.split("/").slice(1).join("/") || r.fullName
}
