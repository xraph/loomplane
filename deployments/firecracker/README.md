# Firecracker hosts

You can use a Linux host for isolated source builds and stateless applications without Kubernetes. Each workload runs in its own microVM. The host agent uses the Firecracker Go SDK; the guest runs containerd and nerdctl.

This backend needs Linux/KVM qualification on your hardware before you use it for untrusted workloads. Cross-compilation and unit tests do not prove network isolation, guest boot, or crash recovery. Use a disposable host for acceptance.

## Install the host

You need root access, `/dev/kvm`, cgroup v2, IPv4 forwarding, `ip`, `nft`, and `mkfs.ext4`. Reserve an unused RFC1918 subnet and a UID range starting at 10000 or higher. The host must have capacity for the serving VM and its replacement at the same time.

Install Firecracker and its matching jailer. Install the CNI `ptp`, `host-local`, and `tc-redirect-tap` binaries in one directory. Record the SHA256 of every binary, the kernel, and the guest image in your configuration. Keep those files and their parent directories owned by root, without group or world write access. Use real paths rather than symlinks.

Build the agent for the host architecture:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o loomplane-host-agent ./cmd/loomplane-host-agent
```

Use `arm64` for an ARM host. Copy the binary to `/usr/local/bin/loomplane-host-agent` and the supplied unit to `/etc/systemd/system/loomplane-host-agent.service`. The unit leaves running VMs intact when the agent restarts.

Create `/etc/loomplane/firecracker.json`, owned by root with mode 0600. `HostKey` is a random 32-byte key encoded as 64 hexadecimal characters. Keep it across restarts. Configure these fields:

| Fields | Value |
| --- | --- |
| `HostID`, `Listen` | A stable host identifier and the control listener, such as `192.0.2.10:9443` |
| `StateDir`, `RuntimeDir` | Dedicated absolute directories, such as `/var/lib/loomplane-firecracker` and `/srv/loomplane-firecracker` |
| `TLSCertificate`, `TLSKey`, `ClientCA` | PEM file paths for the host's HTTPS certificate and trusted client CA |
| `Firecracker`, `Jailer`, `Kernel`, `RootFS` | Objects containing `Path` and `SHA256` |
| `CNIBinDir`, `CNICacheDir` | CNI binaries and this agent's dedicated CNI cache |
| `CNIHashes` | SHA256 strings keyed by `ptp`, `host-local`, and `tc-redirect-tap` |
| `Subnet`, `DNS` | An unused private IPv4 subnet and one to four public IPv4 DNS resolvers |
| `EgressCIDRs` | Public IPv4 destinations builds and apps may reach; an empty list denies outbound traffic |
| `DeniedCIDRs` | Control-plane and management networks that guests must never reach; at least one is required |
| `UIDBase` | First UID in the reserved per-VM range |
| `Limits` | `VMs`, `VCPUs`, `MemoryMiB`, and `DiskMiB` totals; memory includes 128 MiB host overhead per VM |
| `Peers` | Objects containing the client leaf certificate's SHA256 `Fingerprint`, `Org`, and `Connection` |
| `PublishAddress` | The interface address for explicitly published application ports |
| `HTTPListen`, `HTTPSListen`, `AdvertiseHost`, `ACMEEmail` | Optional managed HTTPS listeners, public DNS address, and ACME contact |
| `ACMEDirectory` | Optional ACME directory URL; use a staging CA during qualification |

Run the prerequisite check, then start the service:

```sh
sudo loomplane-host-agent --check /etc/loomplane/firecracker.json
sudo systemctl daemon-reload
sudo systemctl enable --now loomplane-host-agent
```

The check does not boot a VM. Your firewall must restrict the control listener to the control plane. Open application ports only when you intend to publish them. Managed HTTPS needs public ports 80 and 443; configuring a listener does not change your firewall or DNS.

## Build the guest image

Run `build-guest.sh` on a disposable Linux image-builder. Supply `GUEST_BASE` as a digest-pinned Debian-based image containing `sh`, `mount`, `ip`, CA certificates, and the kernel's required userspace. Supply the reviewed `NERDCTL_SHA256` for the matching nerdctl-full 2.4.1 archive, `ARCH`, and a new absolute `OUTPUT` path. The script checks the archive checksum and builds the guest agent from this checkout.

Use a Firecracker-compatible kernel with virtio block/network, vsock, ext4, cgroups, network namespaces, user namespaces, and IP autoconfiguration enabled. The root image is read-only. Container state and build data use the bounded scratch disk; secrets live under `/run`.

Keep the kernel, guest image, Firecracker, jailer, and CNI versions together as a tested set. Changing their hashes changes the reviewed runtime identity and requires a new connection check and plan.

## Connect and deploy

Authorize the client certificate fingerprint and organization in the host's `Peers` configuration first. Leave `Connection` empty during enrollment. This permits capability checks only; workload requests are refused.

Add a **Firecracker host** connection in Integrations. Its credential is JSON containing PEM strings named `ca`, `certificate`, and `key`. Enter the agent's HTTPS endpoint and expected host ID. Copy the new connection ID into the host's `Peers` entry, restart the agent, and recheck the connection before deploying.

Choose `firecracker` as the service provider. You can deploy a digest-pinned OCI image or select a checked Firecracker builder and an external registry with scoped push access. Builds receive the approved source archive and publish an immutable digest. A Firecracker request never falls back to Docker.

A host runs one VM per application instance. Background workers need no listening port. Applications can publish one TCP port at 1024 or above, use an exact managed HTTPS hostname, or remain private. Same-environment apps on the same host resolve `<service>.loomplane`; builds have a separate network scope. DNS updates may take a few seconds after replacement.

Persistent application volumes, GPUs, catalog compositions, horizontal scaling, cross-host private discovery, and wildcard certificates are unsupported. A managed hostname follows replacements within its existing instance. Remove its route before moving the service to another host or replacing that instance. Environment values containing newlines are currently refused by the guest's env-file format.

## Recovery and qualification

Keep the agent database, host key, runtime receipts, and CNI cache together. Do not delete the journal to clear an error. It records ownership and outstanding cleanup. The host refuses ambiguous process ownership instead of signalling an unrelated PID. A stopped app retains its scratch allocation. Removed workloads release it after the process exits. Build VMs have a 40-minute absolute host lifetime, including time spent awaiting source.

Use `go test -tags=acceptance -run TestFirecrackerLive -v ./internal/firecracker` with the disposable-host inputs documented in that test. It exercises the host API and guest runtime. Also verify the full Integrations, plan approval, build logs, release, DNS/TLS, stop/start, and removal flow through Loomplane. A passing host test alone does not establish those platform flows.

Before sharing a host, verify successful allowed traffic and denied host, metadata, IPv6, and cross-environment traffic. Repeat after a VM stops and its address is reused. Exercise an agent restart during launch and a host reboot, and confirm that stale requests cannot mutate the recovered resource. These live checks have not been run on the development Mac.
