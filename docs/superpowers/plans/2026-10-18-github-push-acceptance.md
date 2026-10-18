# GitHub push reanalysis acceptance

Date: October 18, 2026

You can push to a connected repository's default branch and get a new application graph without pressing Analyze. This run used the existing Loomplane GitHub App and xraph installation. App permissions and its webhook destination were unchanged.

## Live evidence

The xraph installation is `171098538`, connected to Loomplane organization `aorg_01m4smcd0fehg8k3050gr3y46b`. The existing project is `proj_01m4va49zwe9pvk24sm9h8v0ty`, with source `src_01m4va4axges683fdtgc1qxx25` tracking `xraph/loomplane`'s default branch.

The relay and analysis worker were stopped when verification began. Restarting those development processes restored delivery. A redelivery during an API reload reached Smee but its local forwarding request failed; repeating it after the API and worker were healthy returned HTTP 202 with `analyses_started: 1` and completed analysis `anl_01m57sn1zdetjaf06q5tavzhfv` at the then-current branch head `be673fbfcc5ab4164cbf8cb99b3c211c4f501371`.

A fresh Push then verified the full path:

| Check | Observed result |
| --- | --- |
| GitHub event | Delivery `9caa70d2-cb07-11f1-95cc-3399e20da3d9`, `refs/heads/main`, commit `5b5d5b37a01aa1cf7fd47b432119f0ed95012d50` |
| Local signed delivery | HTTP 202, `analyses_started: 1` |
| Automatic analysis | `anl_01m57snavken992183xe3yjbhq`, `created_by: github-push` |
| Result | Succeeded at the pushed commit, six services and five deployable services |
| Authentication | A forged SHA-256 signature returned HTTP 401 |

GitHub, Smee, the local API, database and analysis worker were real. The live test used no fake GitHub payload or repository fixture. Application HTTP results and persisted analysis metadata establish this backend flow; no browser UI verification is claimed for this run.

Concurrent development reloads later interrupted the primary API and worker. The final check used loopback-only verification replicas built from committed main at `f322bbec4ae47d90453b7b66a0221b6a3bf42cca`, with the same database and existing GitHub App configuration. Only the local relay target changed. A fresh Push of `67261f28255e6a75753b65d749cc2c788425cc28` reached those replicas as delivery `34b2f9f0-cb0b-11f1-9c24-9d8657c58282`, returned HTTP 202 with one analysis, and completed `anl_01m57v4e7ge51vds4vsvavk3q0` at that exact commit.

## Automated checks

The private-repository integration test now advances a fixture repository to a different commit and different application graph, delivers a signed Push through the actual route, and waits for the separate analysis worker. It checks the new graph and commit through the authenticated API and confirms the earlier analysis stays unchanged. GitHub is the external fixture in this repeatable test; authentication, Postgres, queue, worker, archive discovery and graph composition are real.

Go and dashboard CI use `xraph/workflows` v1.19.0, pinned to `adbc00609c913abaabd7b38d11533e0a44b92432`. Integration, parser fuzzing, generated-client drift and container builds retain their repository-specific jobs. Integration-tagged Go files remain included in lint through `.golangci.yml`. The existing configured gosec rules still gate lint; the shared standalone scan reports additional findings. Reachable vulnerability checks remain blocking.

## Verification and limits

Formatting, `make lint` (zero issues), `actionlint`, shared-workflow input validation and generated-client checks passed. The initial focused GitHub/webhook race run passed in 13.109 seconds; the final focused race run against the committed-source overlay passed in 35.328 seconds. The full integration suite passed in 298.370 seconds. `make build` passed after the concurrent dashboard type error was corrected.

Go unit race tests also passed against committed `main` at `f322bbec4ae47d90453b7b66a0221b6a3bf42cca`, with this task's updated GitHub integration test retained. A read-only Go overlay excluded other tasks' in-progress source edits. The checkout stayed on main throughout.

The ordinary full-suite attempts encountered concurrent changes outside this task: an unfinished `copyDockerOutput` implementation prevented server tests from building, and deployment UI tests failed or timed out. The affected UI tests were `configures a service before creating a plan from its saved manifest`, `does not deploy a different project when an explicit project preset is unavailable`, and `saves an owned target, builder and registry while preserving the build context`. Those files were left with their owners. These mixed-checkout attempts are not passing evidence.

The independent review found no Critical or Important issue in the scoped changes. The CI migration was pushed as `91678ad`; its hosted run was superseded and cancelled by later main commits, so it does not establish a hosted CI pass.
