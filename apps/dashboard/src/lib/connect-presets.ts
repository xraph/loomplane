// The connections you run yourself (an SSH server with Docker, an image
// registry, a Kubernetes cluster) get a page of their own instead of the
// connect dialog: presets that fill the usual values, the commands that
// fetch the rest, and the request the API takes, built from the form.

export type SelfHosted = "server" | "registry" | "kubernetes"

export function isSelfHosted(cloud: string | undefined): cloud is SelfHosted {
  return cloud === "server" || cloud === "registry" || cloud === "kubernetes"
}

export type RegistryAuth = "anonymous" | "password" | "token"

/**
 * Secret is what goes into the credential, kept apart from the settings
 * because the API stores it encrypted and never shows it again.
 */
export type Secret = {
  kubeconfig?: string
  privateKey?: string
  passphrase?: string
  auth?: RegistryAuth
  username?: string
  password?: string
  token?: string
  ca?: string
}

export type Step = {
  title: string
  /** A command to copy, run on the machine it's for. */
  command?: string
  note?: string
}

export type Preset = {
  id: string
  label: string
  description: string
  name: string
  settings: Record<string, string>
  secret?: Secret
  /** Opens the managed registry part of the Kubernetes form. */
  registry?: boolean
  /** How to get what the preset can't fill in. */
  steps: Step[]
}

export const presets: Record<SelfHosted, Preset[]> = {
  server: [
    {
      id: "local",
      label: "This computer",
      description:
        "Docker on the machine Loomplane runs on, over SSH to itself.",
      name: "This computer",
      settings: {
        host: "127.0.0.1",
        port: "22",
        docker_socket: "/var/run/docker.sock",
        advertise_host: "localhost",
        build_workers: "dedicated",
      },
      steps: [
        {
          title: "Turn on SSH",
          command: "sudo systemsetup -setremotelogin on",
          note: "That's macOS. On Linux: sudo systemctl enable --now ssh",
        },
        {
          title: "Make a key for Loomplane and trust it",
          command:
            'ssh-keygen -t ed25519 -N "" -f ~/.ssh/loomplane && cat ~/.ssh/loomplane.pub >> ~/.ssh/authorized_keys',
        },
        {
          title: "Copy the private key, then paste it here",
          command: "pbcopy < ~/.ssh/loomplane",
          note: "Or choose the file. On macOS, Cmd+Shift+. shows ~/.ssh in the file picker.",
        },
        { title: "Your SSH user", command: "whoami" },
        {
          title: "This machine's host key",
          command:
            "ssh-keyscan -t ed25519 127.0.0.1 2>/dev/null | cut -d' ' -f2-",
        },
      ],
    },
    {
      id: "remote",
      label: "Remote Linux server",
      description: "A VPS or a box on your network that already runs Docker.",
      name: "",
      settings: {
        port: "22",
        docker_socket: "/var/run/docker.sock",
      },
      steps: [
        {
          title: "Let your SSH user reach Docker",
          command: "sudo usermod -aG docker $USER",
          note: "Run it on the server, then sign in again.",
        },
        {
          title: "Read the host key on the server itself",
          command: "cat /etc/ssh/ssh_host_ed25519_key.pub",
          note: "Copy it from a console you trust, not from a first SSH connection.",
        },
        {
          title: "Make a key for Loomplane",
          command:
            'ssh-keygen -t ed25519 -N "" -f ~/.ssh/loomplane && ssh-copy-id -i ~/.ssh/loomplane.pub you@your-server',
        },
      ],
    },
  ],
  registry: [
    {
      id: "local",
      label: "Local registry",
      description: "A registry container on this computer, with no sign-in.",
      name: "Local registry",
      settings: { endpoint: "http://localhost:5000", repository: "loomplane" },
      secret: { auth: "anonymous" },
      steps: [
        {
          title: "Start a registry on port 5000",
          command:
            "docker run -d --restart=always -p 5000:5000 --name registry registry:2",
          note: "Plain HTTP is only allowed on localhost.",
        },
      ],
    },
    {
      id: "dockerhub",
      label: "Docker Hub",
      description: "Push to your Docker Hub account.",
      name: "Docker Hub",
      settings: {
        endpoint: "https://registry-1.docker.io",
        auth_endpoint: "https://auth.docker.io/token",
        repository: "",
      },
      secret: { auth: "password" },
      steps: [
        {
          title: "Create an access token",
          note: "On hub.docker.com, under Account settings, Personal access tokens, with Read & Write. Use it as the password.",
        },
        {
          title: "Repository prefix",
          note: "Your Docker Hub username or organization, in lowercase.",
        },
      ],
    },
    {
      id: "ghcr",
      label: "GitHub Packages",
      description: "Push to ghcr.io under a user or organization.",
      name: "GitHub Packages",
      settings: { endpoint: "https://ghcr.io", repository: "" },
      secret: { auth: "password" },
      steps: [
        {
          title: "Create a token",
          note: "A classic personal access token with write:packages. Your GitHub username is the username.",
        },
        {
          title: "Repository prefix",
          note: "The user or organization the images belong to, in lowercase.",
        },
      ],
    },
  ],
  kubernetes: [
    {
      id: "local",
      label: "This computer",
      description: "Docker Desktop, OrbStack, kind, k3d or minikube.",
      name: "Local cluster",
      settings: { namespace: "loomplane", build_workers: "dedicated" },
      steps: [
        {
          title: "Pick your local cluster",
          command: "kubectl config get-contexts",
          note: "Then kubectl config use-context docker-desktop, orbstack, kind-kind or the one you use.",
        },
        {
          title: "Copy a self-contained kubeconfig",
          command: "kubectl config view --minify --flatten --raw | pbcopy",
          note: "--flatten embeds the certificates, which Loomplane needs. On Linux, drop | pbcopy.",
        },
      ],
    },
    {
      id: "bare-metal",
      label: "Bare-metal server",
      description: "One Linux node with no load balancer, like k3s on a VPS.",
      name: "",
      settings: {
        namespace: "loomplane",
        build_workers: "dedicated",
        registry_storage: "local-path",
        registry_network: "node",
      },
      registry: true,
      steps: [
        {
          title: "Copy the kubeconfig from the server",
          command: "sudo cat /etc/rancher/k3s/k3s.yaml",
          note: "That's k3s. Swap 127.0.0.1 in server: for the address Loomplane reaches it on.",
        },
        {
          title: "Point a hostname at the node",
          note: "The registry needs a DNS name, like registry.example.com. Connect its zone under Domains and Loomplane writes the record.",
        },
      ],
    },
  ],
}

// Keys only the managed registry reads, sent only when it's on.
const registryKeys = [
  "registry_hostname",
  "registry_setup",
  "registry_storage",
  "registry_network",
  "ingress_node",
  "load_balancer_addresses",
  "registry_dns",
  "registry_maintenance",
]

/**
 * connectSettings are the settings to send for cloud: each value trimmed,
 * empty ones left out, and those the form's other choices make irrelevant
 * dropped, so a value typed and then hidden isn't sent anyway.
 */
export function connectSettings(
  cloud: SelfHosted,
  values: Record<string, string>,
  registry = false
): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(values)) {
    const t = v.trim()
    if (t) out[k] = t
  }
  if (cloud === "server" && out.host_key) out.host_key = hostKey(out.host_key)
  if (cloud === "kubernetes") {
    // Shared workers need Kata; a dedicated one doesn't.
    if (out.build_workers !== "shared") delete out.build_runtime_class
    if (!registry || !out.registry_hostname)
      for (const k of registryKeys) delete out[k]
    if (out.registry_network !== "node") delete out.ingress_node
    if (out.registry_network !== "metallb") delete out.load_balancer_addresses
  }
  return out
}

/** connectCredential is the credential the API takes for cloud. */
export function connectCredential(cloud: SelfHosted, s: Secret): string {
  switch (cloud) {
    case "kubernetes":
      return s.kubeconfig ?? ""
    case "server": {
      const key = (s.privateKey ?? "").trim()
      return s.passphrase
        ? JSON.stringify({ private_key: key, passphrase: s.passphrase })
        : key && `${key}\n`
    }
    case "registry": {
      const ca = s.ca?.trim() ? { ca: s.ca.trim() } : {}
      if (s.auth === "password")
        return JSON.stringify({
          username: (s.username ?? "").trim(),
          password: s.password ?? "",
          ...ca,
        })
      if (s.auth === "token")
        return JSON.stringify({ token: (s.token ?? "").trim(), ...ca })
      return JSON.stringify(ca)
    }
  }
}

const keyTypes = /^(ssh-|ecdsa-|sk-)/

/**
 * hostKey takes a host key as ssh-keyscan or known_hosts print it, with the
 * host in front, and keeps the key alone, which is what the API checks.
 */
export function hostKey(line: string): string {
  const parts = line.trim().split(/\s+/)
  if (parts.length >= 3 && !keyTypes.test(parts[0]) && keyTypes.test(parts[1]))
    return parts.slice(1).join(" ")
  return line.trim()
}

/** kubeconfigContexts are the contexts a kubeconfig names, in its order. */
export function kubeconfigContexts(yaml: string): string[] {
  const out: string[] = []
  let inContexts = false
  for (const line of yaml.split(/\r?\n/)) {
    // Top-level keys start the line; list items can too, with a dash.
    if (/^[A-Za-z]/.test(line)) {
      inContexts = /^contexts:\s*$/.test(line)
      continue
    }
    if (!inContexts) continue
    // A context's own name sits two spaces in, or right after its dash.
    const m = /^(?:- | {2})name:\s*["']?([^"'\s]+)["']?\s*$/.exec(line)
    if (m) out.push(m[1])
  }
  return out
}

export function currentContext(yaml: string): string {
  return /^current-context:\s*["']?([^"'\s]+)/m.exec(yaml)?.[1] ?? ""
}

/**
 * kubeconfigProblems are why the API would refuse a kubeconfig, found
 * before it's sent, each with what to do about it.
 */
export function kubeconfigProblems(yaml: string): string[] {
  const out: string[] = []
  if (/^\s*exec:\s*$/m.test(yaml))
    out.push(
      "It signs in through a CLI plugin (exec), which Loomplane can't run. Use a service account token or a client certificate."
    )
  if (
    /^\s*(certificate-authority|client-certificate|client-key|tokenFile):\s*\S/m.test(
      yaml
    )
  )
    out.push(
      "It points at files on your disk. Copy it with kubectl config view --minify --flatten --raw to embed them."
    )
  if (/^\s*server:\s*["']?http:\/\//m.test(yaml))
    out.push("The cluster endpoint has to use https.")
  if (/^\s*insecure-skip-tls-verify:\s*true/m.test(yaml))
    out.push(
      "It skips certificate checks. Loomplane keeps them on, so embed the cluster's CA instead."
    )
  return out
}
