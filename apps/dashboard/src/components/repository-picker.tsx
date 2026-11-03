// A repository field: type or paste any GitHub or GitLab repository, or pick
// one the organization's connected GitHub accounts and GitLab connections let
// Loomplane read, from a list grouped by account or namespace. New project and
// Add a repository both use it. The field holds the key a source stores:
// owner/name for GitHub, host/path for anything else.
import { Autocomplete } from "@base-ui/react/autocomplete"
import { GitBranchIcon, LockIcon } from "lucide-react"
import type { ReactNode } from "react"

import {
  useGithubRepositories,
  useGithubStatus,
  useGitlabRepositories,
  useGitlabStatus,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { repositoryKey, startsWithHost } from "@/lib/format"
import {
  fromGitHub,
  fromGitLab,
  type PickerRepository,
  pickerGroupLabel,
  type RepositoryChoice,
  repositoryChoices,
} from "@/lib/integrations"
import { cn } from "@/lib/utils"

type FieldProps = {
  id?: string
  /** A repository's key, or whatever's been typed so far. */
  value: string
  onChange: (value: string) => void
  /** Repositories already chosen elsewhere in the form, as keys. */
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

/** A pasted URL becomes the key the field holds. */
function normalize(typed: string): string {
  return repositoryKey(typed) || typed.trim()
}

/** The github.com/ addon belongs to owner/name, not to another host's key. */
function HostAddon({ value }: { value: string }) {
  if (startsWithHost(value)) return null
  return (
    <InputGroupAddon>
      <InputGroupText>github.com/</InputGroupText>
    </InputGroupAddon>
  )
}

/** What one host's list of repositories is doing. */
type Listing = {
  repositories: PickerRepository[]
  loading: boolean
  error?: unknown
  retry?: () => Promise<unknown>
}

const nothing: Listing = { repositories: [], loading: false }

function GithubListing({ children }: { children: (l: Listing) => ReactNode }) {
  const repos = useQuery(useGithubRepositories)
  return children({
    repositories: (repos.data?.repositories ?? []).map(fromGitHub),
    loading: !repos.data && repos.status !== "error",
    error: repos.status === "error" ? repos.error : undefined,
    retry: repos.refetch,
  })
}

function GitlabListing({ children }: { children: (l: Listing) => ReactNode }) {
  const repos = useQuery(useGitlabRepositories)
  return children({
    repositories: (repos.data?.repositories ?? []).map(fromGitLab),
    loading: !repos.data && repos.status !== "error",
    error: repos.status === "error" ? repos.error : undefined,
    retry: repos.refetch,
  })
}

export function RepositoryPicker(props: FieldProps) {
  const github = useQuery(useGithubStatus)
  const gitlab = useQuery(useGitlabStatus)
  const installations = github.data?.installations ?? []
  const hasGitHub = installations.length > 0
  const hasGitLab = (gitlab.data?.connections.length ?? 0) > 0
  // The lists ask the hosts, so they're only there once an account is
  // connected.
  if (!hasGitHub && !hasGitLab) return <PlainField {...props} />
  const label = pickerGroupLabel(installations)
  const withGitLab = (g: Listing) =>
    hasGitLab ? (
      <GitlabListing>
        {(l) => <Picker {...props} label={label} listings={[g, l]} />}
      </GitlabListing>
    ) : (
      <Picker {...props} label={label} listings={[g]} />
    )
  return hasGitHub ? (
    <GithubListing>{withGitLab}</GithubListing>
  ) : (
    withGitLab(nothing)
  )
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
      <HostAddon value={value} />
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
  label,
  listings,
  ...rest
}: FieldProps & {
  label: ReturnType<typeof pickerGroupLabel>
  listings: Listing[]
}) {
  const all = listings.flatMap((l) => l.repositories)
  const loading = listings.some((l) => l.loading)
  const failed = listings.find((l) => l.error)
  const groups = repositoryChoices(all, label, value, exclude)

  return (
    <div className="grid min-w-0 gap-2">
      <Autocomplete.Root
        items={groups}
        filteredItems={groups}
        value={value}
        onValueChange={(v) => onChange(normalize(v))}
        itemToStringValue={(c: RepositoryChoice) => c.repository.key}
        openOnInputClick
      >
        <Autocomplete.InputGroup render={<InputGroup className={className} />}>
          <HostAddon value={value} />
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
          {loading && (
            <InputGroupAddon align="inline-end">
              <Spinner className="text-muted-foreground" />
            </InputGroupAddon>
          )}
          {end}
        </Autocomplete.InputGroup>
        {all.length > 0 && (
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
                          <Choice key={c.repository.key} choice={c} />
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
      {failed && (
        <QueryError
          error={failed.error}
          fallback="The repositories couldn't be loaded. You can still type a repository."
          retry={() =>
            Promise.all(listings.filter((l) => l.error).map((l) => l.retry?.()))
          }
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

/** name is a repository's name without its owner or namespace, which the group shows. */
function name(r: PickerRepository): string {
  if (r.provider === "gitlab") return r.key.split("/").pop() ?? r.key
  return r.fullName.split("/").slice(1).join("/") || r.fullName
}
