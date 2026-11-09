import { NumberField } from "@/components/platform-cloud-parts"

/** LimitInputs is what is typed in the four limit fields: memory in GiB. */
export type LimitInputs = {
  vcpus: string
  mem: string
  vms: string
  builds: string
}

export type LimitLabels = Record<keyof LimitInputs, string>

const fieldOrder: (keyof LimitInputs)[] = ["vcpus", "mem", "vms", "builds"]

const workspaceLabels: LimitLabels = {
  vcpus: "vCPUs",
  mem: "Memory (GiB)",
  vms: "VMs",
  builds: "Builds at once",
}

/** PlatformLimitFields renders the vCPU, memory, VM and concurrent-build inputs. */
export function PlatformLimitFields({
  values,
  onChange,
  labels = workspaceLabels,
}: {
  values: LimitInputs
  onChange: (next: LimitInputs) => void
  labels?: LimitLabels
}) {
  return (
    <div className="grid grid-cols-2 gap-3">
      {fieldOrder.map((key) => (
        <NumberField
          key={key}
          label={labels[key]}
          value={values[key]}
          onChange={(next) => onChange({ ...values, [key]: next })}
        />
      ))}
    </div>
  )
}
