// Cloud connections made readable: names for each cloud, and which IAM
// role grants a permission a Google Cloud connection is missing.

export const clouds = {
  gcp: {
    label: "Google Cloud",
    credential: "Service account key",
    account: "Project",
  },
  vercel: {
    label: "Vercel",
    credential: "Access token",
    account: "Account",
  },
} as const

export type Cloud = keyof typeof clouds

/** cloudLabel names a cloud for people. */
export function cloudLabel(cloud: string): string {
  return (clouds as Record<string, { label: string }>)[cloud]?.label ?? cloud
}

// The predefined role that grants each permission Loomplane checks, by
// the permission's service.
const rolesByService: Record<string, { role: string; title: string }> = {
  artifactregistry: {
    role: "roles/artifactregistry.admin",
    title: "Artifact Registry Administrator",
  },
  cloudbuild: {
    role: "roles/cloudbuild.builds.editor",
    title: "Cloud Build Editor",
  },
  cloudsql: { role: "roles/cloudsql.admin", title: "Cloud SQL Admin" },
  iam: { role: "roles/iam.serviceAccountUser", title: "Service Account User" },
  resourcemanager: { role: "roles/browser", title: "Browser" },
  run: { role: "roles/run.admin", title: "Cloud Run Admin" },
  secretmanager: {
    role: "roles/secretmanager.admin",
    title: "Secret Manager Admin",
  },
  storage: { role: "roles/storage.admin", title: "Storage Admin" },
}

/**
 * rolesFor lists the predefined roles that grant missing permissions,
 * once each, in the order the permissions came.
 */
export function rolesFor(missing: string[]): { role: string; title: string }[] {
  const out: { role: string; title: string }[] = []
  for (const p of missing) {
    const r = rolesByService[p.split(".")[0]]
    if (r && !out.some((o) => o.role === r.role)) out.push(r)
  }
  return out
}

/** isKeyJSON reports whether text looks like a service account key. */
export function isKeyJSON(text: string): boolean {
  try {
    const k = JSON.parse(text) as { type?: unknown }
    return k.type === "service_account"
  } catch {
    return false
  }
}
