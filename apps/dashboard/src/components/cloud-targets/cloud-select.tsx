import {
  Children,
  isValidElement,
  type ComponentProps,
  type ReactNode,
} from "react"
import { Select, SelectItem } from "@/components/ui/select"

// Base UI needs item labels before the popup mounts. Derive them from the
// actual shared items so asynchronously loaded names and placeholders agree.
export function CloudSelect<Value>({
  children,
  items,
  ...props
}: ComponentProps<typeof Select<Value>>) {
  const labels: { value: unknown; label: ReactNode }[] = []
  const visit = (nodes: ReactNode) =>
    Children.forEach(nodes, (child) => {
      if (!isValidElement<{ value?: unknown; children?: ReactNode }>(child))
        return
      if (child.type === SelectItem)
        labels.push({ value: child.props.value, label: child.props.children })
      else visit(child.props.children)
    })
  visit(children)
  if (
    props.value != null &&
    !labels.some((item) => Object.is(item.value, props.value))
  ) {
    labels.push({
      value: props.value,
      label:
        props.value === "" ? "Choose an option" : "Saved selection unavailable",
    })
  }
  return (
    <Select items={items ?? labels} {...props}>
      {children}
    </Select>
  )
}
