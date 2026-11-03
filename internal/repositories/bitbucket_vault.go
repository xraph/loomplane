package repositories

// BitbucketVaultKeyLabel names the key, derived from secret_key, that Vault
// encrypts Bitbucket refresh tokens with. It's not the cloud credentials'
// key, so the two share no key material.
const BitbucketVaultKeyLabel = "loomplane/bitbucket refresh token v1"

// BitbucketVaultAppID is the Vault app Bitbucket refresh tokens are kept
// under, apart from every other secret.
const BitbucketVaultAppID = "loomplane-bitbucket"
