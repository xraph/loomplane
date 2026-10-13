# Replacing what moves: one contract for every executor

A plan replaces a service or resource when it has to run somewhere it can't be updated in place: another provider, another region, or, for a service, another connection. This design says what a replace does, in what order, what Loomplane keeps track of while it happens, and how a retry, a later deployment or a delete finishes what a failed one left. Every executor gets the same contract, because the runner carries it out, not the executor.

It records how replaces work on `main` as of the merge of `fix/delete-through-binding` (`c57c363`). `docs/architecture.md`, under "When something moves", is the short version for readers of the architecture.

## What changes for you

You move something by changing the manifest, or the connection an environment deploys with. The plan says what it'll do:

```
replace  service:api   api moves from cloud-run to ecs-fargate. The new one starts first, and the old one is removed once it's up.
replace  resource:db   db moves from us-central1 to europe-west1. A region can't change in place, so the new one starts there first, and the old one is removed once it's up.
warning  data-loss     Replacing db provisions a new, empty one, then deletes the old one and its data once the new one is up. Loomplane doesn't move data between them.
```

When the deployment runs, the new one starts with nothing bound to it. Once it's up, and answers if it's public, Loomplane binds it, notes the old one as still to remove, and only then removes the old one, through the executor and connection that deployed it. Services that use the moved one are deployed again in the same plan with its new address.

If removing the old one fails, the new one keeps running and stays bound. The old one stays on Loomplane's list until something removes it: pressing Retry on that deployment, or any later deployment that deploys the same service or resource. Every plan warns about it until it's gone (`still-replaced`).

## Decisions

- **The new one starts first.** Nothing that worked stops working until its replacement answers. The cost is that both run for a while, and for a resource, both bill.
- **Bind before removing.** The binding names the new one as soon as it's up, so a crash or a failed removal never leaves the new one untracked. The old one moves onto a list on that binding, so it's never untracked either.
- **The list lives on the binding, not the deployment.** If you plan again instead of pressing Retry, the next deployment still knows what to remove. A deployment is history; a binding is what runs now.
- **Remove through what deployed it.** The old one goes through its own provider's executor and the connection on its binding, which needn't be the plan's. Moving an environment to another account still cleans up the old one.
- **Data never moves.** Loomplane doesn't copy a database's data to its replacement. A resource never moves between connections at all, since its data would stay behind in an account the environment no longer deploys to.
- **A leftover can't hold every deployment hostage.** A step that fails to remove what *it* replaced fails. A step that fails to remove what an *earlier* move left only warns, so a revoked permission on an old account can't block every deployment from then on.

## When a plan replaces

`planner.diff` sets `Replace` when an action's binding is on another provider, or in another region. `connectionMoves` sets it for a service whose binding names another connection than the one the environment deploys to that cloud with now.

| Change | What the plan does |
|---|---|
| Another provider | `Replace` |
| Another region | `Replace` |
| A service on another connection | `Replace`, removing the old one through the old connection |
| A resource on another connection | `connection-moved` problem: choose the old connection again |
| A resource deleted outside Loomplane, on another connection | `Create` through the new connection, since there's no data to leave behind |
| An AWS region other than the connection's | `connection-region` problem: on AWS, the region is the connection's |

A binding from before bindings recorded their connection names none, so the planner can't tell whether it moved. Cloud Run and ECS find out as they deploy, since a Ctrl Plane instance's provider key names its connection and region. The executor returns an `ElsewhereError` instead of deploying. The runner records the connection on the binding and fails the step, telling you to plan again. The next plan sees the move and replaces it properly, with everything that uses it. Retrying doesn't move it: only a plan that replaces something moves it.

The runner refuses what the planner refuses, in case a plan made before the binding said where it runs reaches it: a move to another connection that its plan didn't replace, and a resource applied through another connection than the one that provisioned it.

## The order

For an action whose binding `moves` (another provider or region, or a service on another connection), `Runner.run` does this:

1. Record the old binding on the step (`Step.Replaced`), before anything starts.
2. Apply the new one with `Job.Binding` nil, since nothing runs the action here yet, and `Job.Replaces` set to the old binding, for an executor whose names could collide with it.
3. If it's a public service, wait for it to answer.
4. If the executor answered with the external ID it was meant to replace, it didn't start another. On another provider or region that's an error and nothing is removed. Through another connection it found the very one it replaces, so there's nothing else to remove.
5. Bind the new one. The binding keeps its ID, and its `Replaced` list gains the old one, with the provider, region and connection that reach it.
6. Remove each entry on `Replaced` through its own executor and connection, taking each off the list as it goes (`SetReplaced`).
7. Clear `Step.Replaced`.

A failure at 2 or 3 leaves the old one bound and running, and the new one, if it started, is found again by name on retry. A failure at 6 leaves the new one bound and the old one on the list.

### Retrying

A retry of the same step reads `Step.Replaced`. When the binding already names something else, the new one is up and bound, so the step skips straight to 6: starting the new one again would only redeploy it.

Applying and removing save their checkpoints apart (`replaceCheckpoint`: `Apply`, `Remove`, and `Removing`, the external ID the removal checkpoint belongs to). A retry hands each its own, so a removal never resumes from what Apply saved, or from another leftover's removal.

### Later deployments

Any deployment of the action runs 6 after its own work, for whatever an earlier move left. Failing to remove one of those adds a warning to the step, which you also see on the next plan. Failing to remove the step's own `Step.Replaced` fails the step, as it would have the first time.

An entry stays on the list unless nothing could ever reach it:

| Why removing failed | The entry |
|---|---|
| The cloud refused, or was down | Stays; a later attempt removes it |
| No executor for its provider, such as a plugin that's been taken out | Stays until it's back |
| The binding names no connection and the plan names none for its cloud | Stays; a plan that pins one removes it |
| Its connection was deleted | Comes off, with a message saying where to delete it yourself |

`stillReplaced` warns about every entry on every plan and pins a connection to the entry's cloud when it can, even when nothing else in the plan deploys there, so the deployment can reach it.

## Deleting

`deleteBound` removes everything on `Replaced` first, then the thing itself, then the binding. Each goes through the connection that deployed it. Only a binding from before Loomplane recorded connections falls back to the plan's, and when the plan names none, the step fails and says to choose one.

A leftover the delete can't remove only warns. The binding goes anyway, and with it Loomplane's record of the leftover, so the warning says where it runs and that it's yours to delete now. The plan says so before you approve it. If the delete's own connection has been removed, the plan warns (`delete-unreachable`), and the deployment drops the binding with a warning saying where it ran, since nothing can reach it again.

The warnings reach the step only once the binding is deleted. If removing the thing itself fails, the binding still tracks everything, and a retry tries again.

## What every executor owes the runner

- **Apply with no binding starts a new one.** It must not find and reuse the one it replaces. Names are where that goes wrong, so each executor picks one the old one doesn't hold:
  - Ctrl Plane (Cloud Run, ECS) keeps instance slugs unique per tenant. `freeInstance` takes the action's name, or the first free one after it: `api-production-x1-2`, then `-3`. A move back takes the first name again once what held it is gone.
  - Cloud SQL, Cloud Storage and S3 put the region in a new instance's or bucket's name, so a region move never collides. One already bound keeps its name.
  - An ECS service gets a target group named after its own instance, so removing the old one in the same account leaves the new one's alone.
- **Remove is safe to repeat.** Something already gone isn't an error. A retry that gets to the removal again finishes it.
- **Remove doesn't forget the action's credentials.** What's kept in Vault under the action's name, such as a database's address and password, is the replacement's now. The runner's removal job makes `Forget` a no-op and `Keep` an error.
- **Say where it runs.** An executor that implements `Locator` describes what a binding names in the provider's own terms, for the message that tells you to delete something yourself. Without one, the message names the external ID, provider and region.

## Errors and warnings

| Code | When |
|---|---|
| `data-loss` | Warning: replacing or deleting a resource deletes its data |
| `still-replaced` | Warning: an earlier move's old one hasn't been removed yet, and what this plan's deployment will do about it |
| `held-address` | A held service uses something this plan replaces, and keeps its old address, which the deployment removes |
| `connection-moved` | A resource would be provisioned through another connection than the one that holds its data |
| `connection-region` | An AWS service or resource names a region other than its connection's |
| `delete-unreachable` | Warning: what a delete removes was deployed through a connection that's been removed |

The runner's own errors say what to do: plan again when a binding turns out to run elsewhere, choose a connection when a legacy binding needs one, and delete something yourself when Loomplane can't reach it.

## Testing

- `internal/deployments/runner_test.go` and `replace_test.go` drive the runner with fake executors per provider. They check the order (the new one applied, bound, and then the old one removed through its own executor and connection), that a failed start removes nothing, that a retry after a failed removal converges without starting the new one again, that each side resumes from its own checkpoint, that removing an old resource keeps the replacement's credentials, that an executor answering with the old external ID removes nothing, and that a delete removes leftovers first and warns about ones it can't.
- `replace_test.go` also covers naming (free Ctrl Plane names, region-aware Cloud Storage and S3 buckets), ECS target groups, `Locate`, and `ElsewhereError`.
- `internal/planning/planner_test.go` covers when a plan replaces, the connection rules, and every warning above.

## Not in this design

- Moving data. A replaced database starts empty. Copy out what you need before you deploy the plan.
- Cutting traffic over gradually. The new one gets every request as soon as what uses it is deployed again with its address.
- Removing something whose connection has been deleted. Nothing can reach it, so Loomplane stops tracking it and tells you where it ran.
