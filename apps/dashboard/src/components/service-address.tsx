import { cn } from "@/lib/utils"

export function ServiceAddress({
  address,
  className,
}: {
  address: string
  className?: string
}) {
  if (!/^https?:\/\//i.test(address))
    return <span className={cn("wrap-anywhere", className)}>{address}</span>
  return (
    <a
      href={address}
      target="_blank"
      rel="noreferrer"
      className={cn(
        "wrap-anywhere underline-offset-4 hover:underline",
        className
      )}
    >
      {address.replace(/^https?:\/\//i, "")}
    </a>
  )
}
