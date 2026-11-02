import { Field, FieldLabel } from "@/components/ui/field"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

export function CloudflareServicesField({
  value,
  onChange,
}: {
  value: string
  onChange: (value: string) => void
}) {
  return (
    <Field>
      <FieldLabel>What to connect</FieldLabel>
      <ToggleGroup
        aria-label="Cloudflare services"
        variant="outline"
        size="sm"
        spacing={1}
        className="flex-wrap"
        value={[value || "dns"]}
        onValueChange={(next) => {
          if (next[0]) onChange(next[0])
        }}
      >
        <ToggleGroupItem value="dns">DNS</ToggleGroupItem>
        <ToggleGroupItem value="r2">R2 storage</ToggleGroupItem>
        <ToggleGroupItem value="dns,r2">DNS and R2</ToggleGroupItem>
      </ToggleGroup>
    </Field>
  )
}
