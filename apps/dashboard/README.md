# Loomplane dashboard

The React dashboard Loomplane serves, with TanStack Router on Vite. It's built from shadcn/ui components on Base UI (preset `b1YmpVjlo`), authsome's own sign-in and organization components, and API clients that `forge client generate` writes from the backend's OpenAPI document.

From the repository root, `make dev` runs it on http://localhost:5173 with the API beside it, and `make build` builds it into `dist/`, which the Go binary serves.

## Working on it

```bash
pnpm dev          # Vite on :5173, proxying /api and /authsome to :8780
pnpm test         # unit tests
pnpm typecheck
pnpm lint
pnpm build
```

Add shadcn components with `pnpm dlx shadcn@latest add <name>`. They land in `src/components/ui`, and you own them from then on.

## Routes

Add routes in `src/routes/`. TanStack's Vite plugin generates `src/routeTree.gen.ts` and splits page components into chunks that load when you visit them. `pnpm typecheck` and `pnpm build` generate the tree before checking TypeScript, so a fresh checkout works without starting the dev server. You can also run `pnpm routes:generate` yourself. Commit the generated tree with your route changes; don't edit or format it by hand.

Use typed links and navigation, including parameters:

```tsx
import { Link, useNavigate } from "@/lib/router"

;<Link to="/projects/$id" params={{ id: project.id }}>
  {project.name}
</Link>

const navigate = useNavigate()
await navigate({ to: "/projects/$id", params: { id: project.id } })
```

The root route keeps Authsome's session checks, pending invitations, and the organization gate around every page. `App.tsx` renders the matched route through an `Outlet` after those checks pass. Unknown addresses show a missing-page screen, and failed page loads offer a reload. The router restores scroll positions as you navigate back and forward.

This uses TanStack Router's Vite plugin. TanStack Start's server rendering and server functions aren't needed by this dashboard: Forge serves the static build and owns the backend. API requests still use the generated Forge clients and their shared cache.

## Calling the API

Every call goes through the generated clients in `src/client/forge`, never `fetch`. You use them with `useQuery` and `useMutation` from `src/client/hooks.ts`:

```tsx
import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"

const list = useQuery(useProjectsList)
```

Bindings named `use...` aren't React hooks: they describe an operation, and `useQuery` subscribes to it. A mutation updates the shared cache and invalidates the lists the backend says it changes, so you rarely refetch by hand.

When the backend's API changes, run Loomplane on :8780, then from the repository root:

```bash
make gen-client     # refresh specs/ and regenerate src/client/forge
make check-client   # fails if the generated code drifted
```

Don't edit anything under `src/client/forge`. `src/client/runtime.ts` is the hand-written part: it routes `/api` and `/authsome` paths to their clients and puts the session on every request.

## Patches

`patches/` holds two fixes to authsome's UI SDK, applied by pnpm. `pnpm-workspace.yaml` says what each one fixes and when to drop it.
