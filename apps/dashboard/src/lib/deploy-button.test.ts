import { expect, test } from "vitest"

import { deployButtonLink, deployButtonMarkdown } from "@/lib/deploy-button"
import { repositoryKey } from "@/lib/format"

test.each([
  ["https://github.com/acme/shop", "https://github.com/acme/shop"],
  ["acme/shop", "https://github.com/acme/shop"],
  ["https://github.com/acme/shop.git", "https://github.com/acme/shop"],
  ["https://gitlab.com/acme/tools/shop", "https://gitlab.com/acme/tools/shop"],
  ["https://bitbucket.org/Acme/Shop", "https://bitbucket.org/acme/shop"],
])("the button for %s names %s", (repository, named) => {
  expect(deployButtonLink("https://loomplane.acme.dev/", repository)).toBe(
    `https://loomplane.acme.dev/new?repo=${named}`
  )
})

test("the link reads back as the same repository on /new", () => {
  const link = new URL(
    deployButtonLink("https://app.loomplane.com", "gitlab.com/acme/tools/shop")
  )
  expect(link.pathname).toBe("/new")
  expect(repositoryKey(link.searchParams.get("repo") ?? "")).toBe(
    "gitlab.com/acme/tools/shop"
  )
})

test("the README snippet is the image linked to the button", () => {
  expect(deployButtonMarkdown("https://app.loomplane.com", "acme/shop")).toBe(
    "[![Deploy with Loomplane](https://www.loomplane.com/deploy-button-dark.svg)](https://app.loomplane.com/new?repo=https://github.com/acme/shop)"
  )
})
