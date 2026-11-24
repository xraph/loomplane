// How one service gets into a workspace service: with the credentials as
// registered, in a database of its own on the same server, or as a user
// with a database of its own that Loomplane makes. Modes the service's
// credentials can't give say why.
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import {
  type AccessChoice,
  accessOptions,
  accessProblem,
  type AccessRights,
} from "@/lib/wiring"

const descriptions = {
  shared: (service: string) =>
    `${service} signs in with the credentials the workspace service was registered with, like everything else that uses them.`,
  own_database: (service: string) =>
    `The same credentials, in a database for ${service} alone on the same server.`,
  own_user: (service: string) =>
    `A user and database for ${service} alone. Loomplane makes them with the admin credential, and takes the login away once ${service} stops using it. The data stays.`,
}

export function ServiceAccess({
  service,
  need,
  rights,
  value,
  ownUser,
  onChange,
}: {
  /** The service that gets in. */
  service: string
  /** The resource's name. */
  need: string
  rights: AccessRights
  value: AccessChoice
  /** The user Loomplane makes, when it's known here. */
  ownUser?: string
  onChange: (choice: AccessChoice) => void
}) {
  const database = value.database.trim()
  const options = accessOptions(rights, database)
  const chosen = options.find((o) => o.access === value.access) ?? options[0]
  const problem = accessProblem(value, rights.engine)
  const redis = rights.engine === "redis"
  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1 text-[13px]">
        How <span className="font-mono font-medium">{service}</span> gets into{" "}
        <span className="font-mono">{need}</span>
      </legend>
      <div
        role="radiogroup"
        aria-label={`How ${service} gets into ${need}`}
        className="grid gap-1.5 sm:grid-cols-3"
      >
        {options.map((o) => {
          const checked = o.access === value.access
          return (
            <button
              key={o.access}
              type="button"
              role="radio"
              aria-checked={checked}
              disabled={!!o.unavailable}
              title={o.unavailable || undefined}
              onClick={() => onChange({ ...value, access: o.access })}
              className={cn(
                "grid gap-0.5 rounded-lg px-3 py-2 text-left ring-1 transition-colors disabled:cursor-not-allowed disabled:opacity-60",
                checked
                  ? "bg-primary/5 ring-primary"
                  : "ring-border hover:bg-muted/50"
              )}
            >
              <span className="text-[13px] font-medium">{o.label}</span>
              <span className="text-[12px] text-muted-foreground">
                {o.unavailable || descriptions[o.access](service)}
              </span>
            </button>
          )
        })}
      </div>
      {value.access !== "shared" && (
        <div className="grid gap-1">
          <label className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <span className="text-muted-foreground">
              {redis ? "Database number" : "Database"}
            </span>
            <Input
              aria-label={`${service}'s database on ${need}`}
              className="h-8 w-44 font-mono text-[12.5px]"
              value={value.database}
              placeholder={redis ? "1" : undefined}
              inputMode={redis ? "numeric" : undefined}
              spellCheck={false}
              onChange={(e) => onChange({ ...value, database: e.target.value })}
            />
          </label>
          {value.access === "own_user" && (
            <p className="text-[12px] text-muted-foreground">
              {ownUser ? (
                <>
                  Signs in as <span className="font-mono">{ownUser}</span>, with
                  a password Loomplane keeps in Vault.
                </>
              ) : (
                "Signs in as a user Loomplane names for it, with a password Loomplane keeps in Vault."
              )}
            </p>
          )}
          {redis && !problem && (
            <p className="text-[12px] text-muted-foreground">
              Redis databases are numbered, and share the server&apos;s
              password.
            </p>
          )}
          {problem ? (
            <p className="text-[12.5px] text-destructive">{problem}</p>
          ) : (
            chosen.note && (
              <p className="text-[12px] text-muted-foreground">{chosen.note}</p>
            )
          )}
        </div>
      )}
    </fieldset>
  )
}
