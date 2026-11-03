# GitLab

Loomplane reads public projects on gitlab.com with no setup. A source is written `gitlab.com/<group>/<project>`, and you can paste the project's URL wherever Loomplane asks for one. Nested groups work (`gitlab.com/acme/platform/api`), and so does a self-managed GitLab on version 17.0 or newer. An older version connects too, with a warning on its card.

To read private projects you register an OAuth application once, and your organization's owners and admins sign in to it from Integrations. For a self-managed GitLab, the operator can register it in config, or an organization can add its own.

## Connecting gitlab.com

In GitLab, open **Preferences**, then **Applications**, then **Add new application**. Put it under a group instead if you'd rather a group own it. Fill in:

| Setting | Value |
|---|---|
| Name | Anything, such as Loomplane |
| Redirect URI | `<public_url>/integrations/gitlab/callback` |
| Confidential | On |
| Scopes | `read_api`, and nothing else |

Save it, then copy the Application ID and the Secret. GitLab shows the secret once.

`config.yaml` already has a `gitlab:` block, wired to environment variables. Set these for both `loomplane` and `loomplane-worker`, and keep the secret out of any file you commit:

```sh
LOOMPLANE_GITLAB_CLIENT_ID="<the Application ID>"
LOOMPLANE_GITLAB_CLIENT_SECRET="<the Secret>"
```

That is how the Docker Compose and App Platform setups take them. On Helm, set `gitlab.clientID` in your values and `secrets.gitlabClientSecret` (or put `LOOMPLANE_GITLAB_CLIENT_SECRET` in the Secret you name in `secrets.existingSecret`). Set both or neither. Loomplane refuses to start with only one. With neither, public projects still work and private ones can't be read. Restart `loomplane` and `loomplane-worker` after you change them.

The Helm values also have `gitlab.apiURL`, `gitlab.allowOrganizationHosts` and `gitlab.webhookURL`. If you'd rather use a file than the environment, put a `gitlab:` block in `config.local.yaml` with `client_id` and `client_secret`. You need that file anyway for the hosts list below.

`gitlab.api_url` (`LOOMPLANE_GITLAB_API_URL`) is the address Loomplane reaches gitlab.com at, for reading projects and for signing in, and it defaults to `https://gitlab.com`. You'd change it only to point at a test double. Sources keep the host name either way, so they're still written `gitlab.com/<group>/<project>`.

## A self-managed GitLab

There are two ways in, and they can live side by side.

### As the operator

List the GitLab under `gitlab.hosts`. Every organization on this Loomplane can then sign in to it. Register an application on that GitLab the same way as above, with the same redirect URI and the same single scope, then add it:

```yaml
gitlab:
  hosts:
    - web_url: https://git.example.com
      client_id: "<the Application ID>"
      client_secret: "<the Secret>"
```

A list can't come from one environment variable, so `gitlab.hosts` has none. Put it in `config.local.yaml`. `web_url` is the GitLab's address with no path, and it may be http or https. Set `client_id` and `client_secret` together, or leave both out to read that GitLab's public projects only. These hosts are yours, so Loomplane trusts them: it doesn't filter the addresses they resolve to, which means a GitLab on your private network works.

Links Loomplane builds to a repository, such as **Open on git.example.com** on a project's sources, always use https. A GitLab you serve over plain http still gets https links, so they only open if that host answers https as well.

### As an organization

Someone who may connect repositories opens **Integrations**, chooses **Add a self-managed GitLab**, and enters its address, an Application ID and a Secret. The dialog shows the callback URL to use when they create the application on their GitLab. They can then sign in to it from the same card.

An operator who doesn't want this sets `gitlab.allow_organization_hosts: false`, or `LOOMPLANE_GITLAB_ALLOW_ORGANIZATION_HOSTS=false`. It defaults to true.

Because anyone with the right role can add one of these, Loomplane is stricter about where it sends requests:

- The address has to be https and has to be a host name of its own. A GitLab served under a path, such as `https://example.com/gitlab`, is refused.
- Loomplane won't call private, loopback or link-local addresses for it, so an organization can't point Loomplane at your internal network.
- It won't follow a redirect to http or to another host.

If your GitLab sits on a private network, add it under `gitlab.hosts` instead.

## Which GitLab user to sign in as

Loomplane acts as the GitLab user who signs in. It can read whatever that user can read, so don't sign in as yourself if your account sees more than Loomplane should.

Create a dedicated GitLab user, give it Reporter access on the groups Loomplane should read, and sign in as that user. Reporter is enough to read code and nothing more. To widen or narrow what Loomplane sees later, change the user's membership in GitLab. No change is needed in Loomplane.

Loomplane seals the tokens in its vault. GitLab rotates them every two hours, and Loomplane renews them as it goes. If GitLab refuses to renew one (the user was removed, or someone revoked the application), the card shows **Sign in again**. Anyone who may connect repositories can fix it by signing in again, and the new sign-in replaces the old one.

Disconnecting a GitLab on its Integrations card revokes the sign-in at GitLab, as well as removing it from the organization.

## Webhooks

A push to a project's default branch can re-analyze it without anyone pressing Analyze. Loomplane only reads from GitLab, so it doesn't add the webhook for you. You add it yourself, once per project or group.

Each connection gets its own URL, and a secret that proves a delivery came from your GitLab. The URL is:

```
<webhook_url>/api/v1/gitlab/webhook/<connection id>
```

`gitlab.webhook_url` (`LOOMPLANE_GITLAB_WEBHOOK_URL`) defaults to your `public_url`. Set it only when GitLab reaches Loomplane at a different address than people do, such as an internal one.

In GitLab, open **Settings**, then **Webhooks**, on the project or on its group. Paste the URL and tick **Push events**. The secret depends on your GitLab version, and the connection's **Webhook** panel on Integrations has a place for each:

- On GitLab 19 and newer, GitLab makes the signing token itself. Choose **Generate signing token** in GitLab and copy it, since GitLab shows it once. Paste it into **Paste GitLab's signing token** on the panel and choose **Save signing token**. Loomplane keeps it and checks the signature on every delivery. You can't paste a token of your own into GitLab, so this one always goes from GitLab to Loomplane.
- On older versions, choose **Create secret** on the panel. Loomplane shows a secret token once. Copy it and paste it into GitLab's **Secret token** field, which GitLab sends back with each delivery.

Replacing either one makes Loomplane forget the old secret, so a hook still using it stops working until you update it in GitLab.

A project webhook covers that project. A group webhook covers every project under the group, which is less to maintain, but group webhooks need GitLab Premium on gitlab.com.

Only a push to a project's default branch re-analyzes. A source with no branch set follows the default, and a source that names a branch is re-analyzed only while that branch is the default. A push to another branch, a tag or a deleted branch starts nothing. Deliveries only reach the organization that owns the connection, even when another organization has projects on the same repository.

An analysis started this way shows `created_by` as `gitlab-push`. To check one, push a change, then compare the finished analysis's `commit_sha` with the commit you pushed.

## Deploying to Render and App Platform

Every deployment target but two builds from the archive Loomplane fetched, so GitLab sources deploy anywhere GitHub ones do. Render and DigitalOcean App Platform are the two that build from the repository themselves, through their own GitLab connection.

For those two:

- Only gitlab.com sources work. A source on a self-managed GitLab can't deploy to either, and the plan says so.
- Connect GitLab in Render's or DigitalOcean's dashboard first, with access to the project. Loomplane can't do that for you.

## Why read_api and not read_repository

GitLab's API can't list projects or read a project's default branch with `read_repository`, and its documentation doesn't say archives work with it. `read_api` does all of that and is still read-only.
