import { describe, expect, it } from "vitest"

import {
  connectCredential,
  connectSettings,
  currentContext,
  hostKey,
  kubeconfigContexts,
  kubeconfigProblems,
  presets,
} from "./connect-presets"

const kubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0t
    server: https://127.0.0.1:6443
  name: kind-kind
contexts:
- context:
    cluster: kind-kind
    namespace: apps
    user: kind-kind
  name: kind-kind
- name: docker-desktop
  context:
    cluster: docker-desktop
    user: docker-desktop
current-context: kind-kind
kind: Config
users:
- name: kind-kind
  user:
    token: abc
`

describe("kubeconfig", () => {
  it("lists contexts and the current one", () => {
    expect(kubeconfigContexts(kubeconfig)).toEqual([
      "kind-kind",
      "docker-desktop",
    ])
    expect(currentContext(kubeconfig)).toBe("kind-kind")
  })

  it("finds nothing wrong with a flattened one", () => {
    expect(kubeconfigProblems(kubeconfig)).toEqual([])
  })

  it("says what the API would refuse", () => {
    const bad = `clusters:
- cluster:
    certificate-authority: /Users/me/.minikube/ca.crt
    server: http://localhost:8080
users:
- name: gke
  user:
    exec:
      command: gke-gcloud-auth-plugin
`
    expect(kubeconfigProblems(bad)).toHaveLength(3)
  })
})

describe("connectSettings", () => {
  it("trims, drops empties and keeps the host key alone", () => {
    expect(
      connectSettings("server", {
        host: " 127.0.0.1 ",
        port: "",
        host_key: "127.0.0.1 ssh-ed25519 AAAAC3 comment",
      })
    ).toEqual({ host: "127.0.0.1", host_key: "ssh-ed25519 AAAAC3 comment" })
  })

  it("leaves out what Kubernetes choices hide", () => {
    const values = {
      namespace: "loomplane",
      build_workers: "dedicated",
      build_runtime_class: "kata",
      registry_hostname: "registry.example.com",
      registry_network: "node",
      ingress_node: "n1",
      load_balancer_addresses: "10.0.0.0/30",
    }
    expect(connectSettings("kubernetes", values, false)).toEqual({
      namespace: "loomplane",
      build_workers: "dedicated",
    })
    expect(connectSettings("kubernetes", values, true)).toEqual({
      namespace: "loomplane",
      build_workers: "dedicated",
      registry_hostname: "registry.example.com",
      registry_network: "node",
      ingress_node: "n1",
    })
  })
})

describe("connectCredential", () => {
  it("builds registry JSON from the sign-in chosen", () => {
    expect(connectCredential("registry", { auth: "anonymous" })).toBe("{}")
    expect(
      JSON.parse(
        connectCredential("registry", {
          auth: "password",
          username: " me ",
          password: "pw",
        })
      )
    ).toEqual({ username: "me", password: "pw" })
    expect(
      JSON.parse(connectCredential("registry", { auth: "token", token: "t" }))
    ).toEqual({ token: "t" })
  })

  it("wraps an encrypted SSH key with its passphrase", () => {
    expect(connectCredential("server", { privateKey: "KEY" })).toBe("KEY\n")
    expect(
      JSON.parse(
        connectCredential("server", { privateKey: "KEY", passphrase: "p" })
      )
    ).toEqual({ private_key: "KEY", passphrase: "p" })
  })
})

it("keeps a bare host key as it is", () => {
  expect(hostKey("ssh-ed25519 AAAAC3")).toBe("ssh-ed25519 AAAAC3")
})

it("starts every self-hosted form with a local preset", () => {
  expect(presets.server[0].settings.host).toBe("127.0.0.1")
  expect(presets.registry[0].settings.endpoint).toBe("http://localhost:5000")
  expect(presets.kubernetes[0].id).toBe("local")
})
