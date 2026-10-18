# Gateway qualification

The generated gateway pins Bastion `v1.2.3-0.20261013131827-23c004a37e64`, commit `23c004a37e648d796a832a333a58ac10f02e378c`, module checksum `h1:7supafY9YLLMaYN/5TJcXi1pmc8e8LVSK8OJhsGW3Gw=`. Qualification uses `internal/gateway/program/go.mod.txt` and its sum file, never a newer sibling checkout.

Run `go test ./internal/gateway -run 'TestFederationGatewayQualification|TestSource_routes|TestSource_builds' -count=1 -v`.

| Requirement                                  | State                                    | Evidence or gate                                                                                                                                                                    |
| -------------------------------------------- | ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Existing HTTP routes                         | supported                                | `TestSource_routes` exercises the generated gateway against two local upstreams. This proves routing, not private authorization.                                                    |
| Verified client SAN identity                 | unsupported in current generated gateway | `main.go.txt` configures HTTP routes without a client certificate verifier or a grant authorizer.                                                                                   |
| Credential rotation preserving private trust | unsupported on inspected TLSManager path | The qualification fixture proves `Reload` returns success and leaves `RootCAs` nil after the configured CA file disappears. System roots are not the intended private trust bundle. |
| HTTP/2 and gRPC with private mutual TLS      | unverified                               | `proxy/grpc.go` has its own transport; its constructor does not load the configured CA/client key files. End-to-end identity and trailers still need tests.                         |
| Immediate revocation of active streams       | unverified                               | No federation policy or stream registry exists in the generated program. Ordinary HTTP shutdown does not establish revocation semantics.                                            |
| Atomic policy and route replacement          | unsupported in current generated gateway | Routes are fixed at process startup. A synchronized individual route update does not establish a whole-policy transaction.                                                          |

For the first private slice, retain Bastion only behind a verified listener and immutable policy snapshot boundary. Use Go's standard TLS certificate validation with SPIFFE/SPIRE workload credentials and explicit authorized URI SAN matching. Do not build a certificate authority. Keep the previous validated configuration when a replacement is invalid, and close revoked streams through a tracked connection registry. These are implementation decisions for Tasks 5-8, not qualified features.

Before enabling gRPC, prove client and upstream mutual TLS, bidirectional streaming, trailers, rotation, expired trust and revocation together. If the pinned transport cannot meet those tests, qualify a standard Envoy/SPIFFE integration or a supported Bastion update. This milestone does not select an untested transport as production support.
