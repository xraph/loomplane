// The sidebar links the cloud edition adds. JSX keeps the router's typed
// links checking the page address and its parameter.
import { Link } from "@/lib/router"

export const workspaceBillingLink = (
  <Link to="/settings/$page" params={{ page: "billing" }} />
)

export const platformBillingLink = (
  <Link to="/platform/$page" params={{ page: "billing" }} />
)
