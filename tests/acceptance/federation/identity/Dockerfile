FROM ghcr.io/spiffe/spire-agent:1.15.4@sha256:adc9e89e171af5764b29f9ffc9d3296b8033ba8c6ca11bcadbb56099170d526e AS agent
FROM ghcr.io/spiffe/spire-server:1.15.4@sha256:a728418a76e5054e977680a7c70a291f93389294e4bef00e2aaad65324f39357
COPY --from=agent /opt/spire/bin/spire-agent /opt/spire/bin/spire-agent
COPY identity-fixture /identity-fixture
USER 0
ENTRYPOINT ["/identity-fixture"]
