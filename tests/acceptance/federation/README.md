# Federation acceptance

G0 is open. Offline gateway characterization and documented Miget constraints do not prove private cross-cloud communication.

Before live probes, record the test workspace/account, two destination identities, disposable resource inventory, spending limit, expiry and cleanup owner. Use explicit resource IDs for cleanup. Never delete resources by a broad name prefix.

1. Capture scoped reads and denials with workspace credentials. Prove pagination and exact resource identity.
2. Deploy an immutable application revision with provider automation disabled; replace it and verify readiness and log redaction.
3. Try the same credential from a neighboring application. Prove private backend denial over IPv4, IPv6 and public ports.
4. Resolve the service from both networks before and after replacement. Record addresses, TTL, readiness and any required restarts.
5. Import an existing network path and probe each direction. A working tunnel grants no service access.
6. Exercise grant expiry, revocation, restart and control-plane outage with long-lived streams. Retain denial evidence.
7. Remove owned resources, verify absence and release gateway addresses separately. Record any unresolved cleanup reference.

For each check, retain the command, sanitized output, timestamp and result (`supported`, `unsupported` or `unverified`). Fixtures must state whether they came from live capture or synthetic input. Never promote documentation-only observations to supported capabilities.
