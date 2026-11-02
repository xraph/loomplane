# Shared Cloud live acceptance

Prepare the executable and review its scope before you authorize a run:

```sh
python3 tests/acceptance/shared-clouds/live.py prepare --state .superpowers/shared-cloud-live/state.json
python3 tests/acceptance/shared-clouds/live.py inspect --state .superpowers/shared-cloud-live/state.json
```

Preparation reads the selected DigitalOcean account and Vercel team through the existing primary connection credentials. It checks the current size, price and DNS inventory, compiles the integration entrypoint, and writes a private scope receipt. It creates no cloud resources and starts no tunnel. The primary app stays on port 8780.

You must explicitly approve the printed scope SHA256 before running this command. Replace `APPROVED_SCOPE_SHA256` with that exact hash:

```sh
python3 tests/acceptance/shared-clouds/live.py run --state .superpowers/shared-cloud-live/state.json --approve-scope APPROVED_SCOPE_SHA256
```

The scope fixes two DigitalOcean `s-1vcpu-2gb` hosts in `nyc3`, Ubuntu 24.04, no backups or extra volumes, a six-hour deadline and less than $1 in compute. Three unique children of `shipbraid.dev` use the selected Vercel team. Each child is a separate granted namespace. A fresh TXT challenge at `_loomplane.<child>.shipbraid.dev` must resolve publicly before its route can reserve a hostname. Provider hosting metadata supplies only the exact hosted-zone/account identity. The scope includes these three TXT records, their TTL of 60 seconds and per-run ownership tags. Their write intents and exact receipts remain in the cleanup ledger alongside the three A records. No apex proof, nameserver change or existing proof record is permitted. The run uses an isolated application database. Provider credentials and SSH private keys stay in memory or the isolated app's encrypted Vault; the harness doesn't write raw credentials to its receipts or logs.

Cloudflare Quick Tunnel carries the single-use enrollment token and public SSH host key to an enrollment-only proxy. The proxy permits only persisted organization/operation POST paths, limits bodies to 16 KiB and requests to ten seconds, and starts with an empty allowlist. The upstream is an isolated loopback API. The tunnel closes after both enrollments or its 45-minute limit.

The live assertions cover public DNS and trusted public HTTPS for three distinct project/release markers, current-release migration, a future release on the destination, and unrelated-service continuity. Pinned SSH plus exact container and instance identities distinguish the intended backend. A transparent observer records the actual Vercel PATCH response time, and source probes cover the old record's cache lifetime independently of the product deadline. Private ACME fixture results remain separate.

You can inspect and clean an interrupted run independently:

```sh
python3 tests/acceptance/shared-clouds/live.py inspect --state .superpowers/shared-cloud-live/state.json
python3 tests/acceptance/shared-clouds/live.py cleanup --state .superpowers/shared-cloud-live/state.json
```

Each worker, tunnel and proxy starts behind a private pipe gate. The supervisor persists launch intent, waits up to ten seconds for the running gate wrapper to report readiness, then records its exact process identity before releasing it. A temporary interpreter name observed immediately after process creation cannot become the cleanup identity. Parent loss before release closes the pipe and exits the gate without starting its target. Cleanup refuses provider work when a launch intent has no completed identity receipt.

Cleanup first stops the exact recorded worker, tunnel and proxy processes. It reconciles known ownership intents, checks account and resource identity, consumes the latest owned DNS receipt after Vercel ID rotation, and verifies the surviving DNS inventory against the baseline. Unknown resources and changed ownership are preserved. Network or authentication errors leave cleanup unresolved. An attempted run cannot create hosts again; recover its receipts with `inspect` and finish with `cleanup`.

DigitalOcean can retain the two operation/spec tags created for each host. The ledger records their exact names before apply, and cleanup preserves them. The [DigitalOcean tags API](https://docs.digitalocean.com/reference/api/reference/tags/) filters resource counts by the token's read permissions, so an empty count cannot establish that unknown resources do not use a tag. Compute and DNS absence are reported separately from these possible four retained metadata tags.

The supervisor reserves the final five minutes for shutdown and cleanup. It enforces the deadline while this machine and process are running. A machine outage or supervisor kill can interrupt enforcement. Keep the state directory and run independent cleanup when the machine returns. The command lock prevents simultaneous workers and cleanup; stop an active supervisor first. The isolated database is dropped only when its recorded cluster identity, name and PostgreSQL OID match.

You need Go, Python 3.11+, PostgreSQL, and the verified cloudflared binary recorded during task preparation. `LOOMPLANE_TEST_DATABASE_URL` selects the local database administrator connection; its default matches the repository's compose PostgreSQL on port 55532. The harness loads primary configuration locally to reveal only the two selected provider credentials in memory. It doesn't print configuration or process environments.

Run the preparation safety checks without live effects:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/acceptance/shared-clouds -p 'test_*.py'
GOMAXPROCS=2 go test -race -tags=integration -run '^TestSharedCloudLive|^TestSharedCloudCombinedDrainBound$' -count=1 ./tests/integration/
```
