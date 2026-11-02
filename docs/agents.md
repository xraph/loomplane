# Loomplane for AI agents

`loomctl mcp` serves Loomplane's tools over the Model Context Protocol, so an agent such as Claude Code can analyze a project, write its `loomplane.yaml`, plan a deployment, read what the plan would do and, if you let it, approve it.

## Setting it up

Make an API key under Settings, API keys. Then add loomctl to your agent's MCP configuration:

```json
{
  "mcpServers": {
    "loomplane": {
      "command": "loomctl",
      "args": ["mcp"],
      "env": {
        "LOOMPLANE_URL": "https://loomplane.example.com",
        "LOOMPLANE_API_KEY": "lp_live_..."
      }
    }
  }
}
```

For Claude Code, that's `claude mcp add loomplane --env LOOMPLANE_URL=... --env LOOMPLANE_API_KEY=... -- loomctl mcp`.

## What it can do

| Tool | What it does |
|---|---|
| `whoami` | Who the key acts as, and its permissions |
| `list_projects` | The organization's projects |
| `analyze` | Reads a project's repositories, or the ones named in `sources`; waits until each is done |
| `get_manifest`, `draft_manifest`, `save_manifest` | Reads, drafts and saves `loomplane.yaml` |
| `create_plan`, `list_plans`, `get_plan`, `reject_plan` | Plans an environment and reads the result. `scope` names the repositories a plan moves; the rest stay as deployed. `from` promotes what another environment runs instead of planning the manifest |
| `get_release_watch` | How a deployment is judged after it finishes: its status and verdict, each service's verdict, the services it can't watch and any rollback plan. Read-only. A deployment with no watch answers not found, and `unverified` means missing evidence, so an agent should ask a person |
| `list_deployments`, `get_deployment`, `deployment_for_plan` | Follows deployments step by step |
| `status` | What runs where, with addresses and drift |
| `list_variables`, `set_variable` | Variables; a secret's value is never shown |
| `approve_plan`, `retry_deployment`, `create_rollback` | Only with `--allow-deploy`. `create_rollback` plans an environment back to its previous successful release and takes a `reason`. Data-bearing resources stay as they are, and nothing changes until the plan is approved |

The manifest's JSON schema is the resource `loomplane://schema/loomplane.yaml`, so an agent writing one knows every field. A manifest that doesn't parse comes back as an error listing each problem with its line, which an agent can fix and save again.

## What it can't do

Three things decide what an agent may do, and all three have to agree.

The key comes first. An agent acts as the person who made its key, with their role, in one organization. A read-only key can list, read and follow, and the API refuses anything else, whatever the agent asks.

Deploying is off by default. Without `--allow-deploy`, an agent can analyze, edit the manifest and plan, and approving stays with a person in the dashboard or with `loomctl approve`. `approve_plan` and `retry_deployment` change your clouds, and `create_rollback` starts a rollback, so they're only offered when you start the server with `--allow-deploy`, and they're marked destructive for clients that ask before running those.

An approval names the plan's revision. `approve_plan` takes the revision the agent read with `get_plan`. If what's deployed changed since, the plan comes back stale and nothing happens, so an agent can't approve a plan it hasn't seen.

What an agent changes is in the audit trail, under the person who made its key.
