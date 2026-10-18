# Deployment flow verification, October 18, 2026

You can start from New deployment anywhere in Loomplane. Choose a project and environment, configure services and infrastructure, save the changed placements, then review the plan. Approval uses the exact manifest revision and repository commits shown in that plan.

The new page shares service placement, account selection, source scope and draft tracking with the project's Deploy tab. It reads the real provider catalog and offers compatible targets. Account credentials stay in Integrations. A first configuration comes from repository analysis and is saved conditionally, so it cannot overwrite a manifest someone else saved while you were reviewing the suggestion.

## Browser run

The test used the existing isolated project `Loomplane Vercel E2E`, its production environment and the connected Vercel account named `Test`. Its public source was `aurorascharff/astro-demo`, pinned to `71eae662d03453c3b8333f59de368783828395d0`. Only the public sample source was uploaded.

| Boundary | Evidence |
| --- | --- |
| Configure and save | Enabled `astro-demo` for production. Review stayed disabled until its placement was saved as manifest revision 9. |
| Plan | `plan_01m57ta18ceab9q05tve67dx9y` selected Test (Vercel), with one creation and no updates, replacements or deletions. |
| Approve and deploy | `dep_01m57td0awewzvaczkv4fmrhp7` succeeded. The service build/deploy step took 26 seconds. |
| Build output | Loomplane displayed 95 retained build lines and the generated HTTPS URL. |
| Served application | The home page loaded. Navigation to Islands worked, and the Svelte counter changed from 0 to 2. |
| Cleanup configuration | Disabled only `astro-demo` in production and saved manifest revision 10. |
| Cleanup plan | `plan_01m57tj47mefbbgsqxtmmgmea3` contained one deletion and no creations, updates or replacements. |
| Cleanup deployment | `dep_01m57tmm0xeyp9wzqswcszqf2a` succeeded. |
| Cleanup result | Infrastructure showed zero Vercel services and resources. Reloading the old URL returned `404 DEPLOYMENT_NOT_FOUND`. |

The shared development API was restarting during concurrent backend edits. The first placement save and approval attempt received Bad Gateway responses. The placement was retried after the API returned; the plan was read again to confirm approval had not committed. Verification then used a stable copy of the built API on port 8782 and the dashboard on port 5185, against the same database and test project. The normal development server continued on its existing ports.

This run covers Vercel static-site deployment through Loomplane. Other providers, server workloads and workers retain their existing tests and qualification requirements.

## Code checks

The new behavior tests failed before implementation, then passed after the changes. They cover navigation with project/environment presets, saved service placements, repository scope, first configuration, unavailable destinations and draft guards. Hiding YAML preserves both the draft and the Review lock. Project selection stays locked while a plan request is pending.

All 232 dashboard tests passed with `pnpm --dir apps/dashboard test --maxWorkers=1`. The parallel runs hit existing five-second test limits under concurrent build load; the assertions and timeouts stayed unchanged. `make lint-dashboard` and the dashboard production build passed. This repository has no `make l` or `make f`; the checks used `make lint` and `pnpm --dir apps/dashboard format` instead.

Repository-wide Go lint reports nine `gocritic` findings in concurrent PostgreSQL/recovery work outside this change. `make test` fails to compile the concurrent planning tests because `AccessGrant.ExportRevision` is missing. The database integration run was started separately. These checks do not establish qualification for the unrelated backend changes.

The real page was inspected at the browser's current desktop size. The browser ignored requested viewport overrides, so a narrow-screen rendered check remains unverified.
