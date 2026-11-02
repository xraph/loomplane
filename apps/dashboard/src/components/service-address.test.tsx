import { render, screen } from "@testing-library/react"
import { expect, test } from "vitest"
import { ServiceAddress } from "@/components/service-address"

test.each(["http://app.example.test", "https://app.example.test"])(
  "opens the web endpoint %s",
  (address) => {
    render(<ServiceAddress address={address} />)
    expect(
      screen.getByRole("link", { name: "app.example.test" })
    ).toHaveAttribute("href", address)
  }
)

test.each([
  "memcached://cache:11211",
  "redis://cache:6379",
  "postgresql://db:5432",
  "javascript:alert(1)",
])("shows %s as an address without a browser link", (address) => {
  render(<ServiceAddress address={address} />)
  expect(screen.getByText(address)).toBeVisible()
  expect(screen.queryByRole("link")).not.toBeInTheDocument()
})
