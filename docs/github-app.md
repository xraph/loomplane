# GitHub App

Loomplane reads public GitHub repositories with no setup at all. To read private ones, it needs a GitHub App that your organization's admins install on their GitHub accounts. You create the App once, for the whole Loomplane instance.

## Create the App

On GitHub, go to **Settings → Developer settings → GitHub Apps → New GitHub App** (under an organization's settings if the App should belong to the organization), and fill in:

| Setting | Value |
|---|---|
| GitHub App name | Anything; its slug goes in `LOOMPLANE_GITHUB_APP_SLUG` |
| Homepage URL | Your Loomplane's public URL |
| Callback URL | `<public_url>/integrations/github/setup` |
| Request user authorization (OAuth) during installation | **On** |
| Expire user authorization tokens | On |
| Webhook | **Off** (uncheck Active); Loomplane doesn't use webhooks yet |
| Repository permissions: Contents | **Read-only** |
| Repository permissions: Metadata | Read-only (GitHub requires it) |
| Everything else | No access |
| Where can this GitHub App be installed? | Any account, unless only your own organization uses this Loomplane |

Create it, then on its page:

1. Note the **App ID** and the **Client ID**.
2. Generate a **client secret**.
3. Generate a **private key**. GitHub downloads a `.pem` file.

## Configure Loomplane

Set these for both `loomplane` and `loomplane-worker`. Keep the secret and the key out of any file you commit:

```bash
LOOMPLANE_GITHUB_APP_ID=123456
LOOMPLANE_GITHUB_APP_SLUG=your-app-slug
LOOMPLANE_GITHUB_CLIENT_ID=Iv23li...
LOOMPLANE_GITHUB_CLIENT_SECRET=...
LOOMPLANE_GITHUB_PRIVATE_KEY_FILE=/run/secrets/github-app.pem
```

`LOOMPLANE_GITHUB_PRIVATE_KEY` takes the PEM itself instead of a path, which suits a secret store that only does variables. Newlines written as `\n` work. Loomplane refuses to start with some of these set and not the others, and with a key it can't parse.

For GitHub Enterprise Server, also set `LOOMPLANE_GITHUB_API_URL` to `https://<host>/api/v3` and `LOOMPLANE_GITHUB_WEB_URL` to `https://<host>`.

## Connect an account

An owner or admin opens **Integrations** in the dashboard and chooses **Connect GitHub**. GitHub asks which account to install on and which repositories to allow, then sends them back to Loomplane. From then on, any project in the organization whose repository that installation can see is analyzed with access to it. Choosing repositories later happens in the App's settings on GitHub.

## What Loomplane does with it

- **Connecting.** GitHub's redirect carries the installation's number and a one-time code. Loomplane exchanges the code for the installer's own token and checks that their account can see that installation. Only then does it connect the installation to the organization. The number alone proves nothing, since anyone can guess one. The state in the link is signed, lasts 15 minutes, and names the organization and person who started it. The installer's token is used for that one check and dropped.
- **Analyzing.** For each analysis, the worker mints an installation token limited to the one repository and to reading its contents. GitHub expires it within the hour. Loomplane stores no token anywhere, and uses an organization's own installations only, so one organization can never read another's code.
- **Disconnecting** removes the installation from the organization in Loomplane. The App stays installed on GitHub until someone uninstalls it there.
