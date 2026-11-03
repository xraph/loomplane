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

Put them in `config.yaml` (or an ignored `config.local.yaml`), and keep the secret out of any file you commit:

```yaml
gitlab:
  client_id: "<the Application ID>"
  client_secret: "<the Secret>"
```

Set both or neither. Loomplane refuses to start with only one. With neither, public projects still work and private ones can't be read. Restart `loomplane` and `loomplane-worker` after you change them.

`gitlab.api_url` is the address Loomplane reads gitlab.com from, and it defaults to `https://gitlab.com`. You'd change it only to point at a test double.

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

`web_url` is the GitLab's address with no path, and it may be http or https. Set `client_id` and `client_secret` together, or leave both out to read that GitLab's public projects only. These hosts are yours, so Loomplane trusts them: it doesn't filter the addresses they resolve to, which means a GitLab on your private network works.

### As an organization

Someone who may connect repositories opens **Integrations**, chooses **Add a self-managed GitLab**, and enters its address, an Application ID and a Secret. The dialog shows the callback URL to use when they create the application on their GitLab. They can then sign in to it from the same card.

An operator who doesn't want this sets `gitlab.allow_organization_hosts: false`. It defaults to true.

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

Each connection gets its own URL, and a secret that its card shows once. The URL is:

```
<webhook_url>/api/v1/gitlab/webhook/<connection id>
```

`gitlab.webhook_url` defaults to your `public_url`. Set it only when GitLab reaches Loomplane at a different address than people do, such as an internal one.

In GitLab, open **Settings**, then **Webhooks**, on the project or on its group. Paste the URL, tick **Push events** and paste a secret. The card gives you two, because GitLab checks deliveries in two ways:

- On GitLab 19 and newer, GitLab signs each delivery, and you paste the signing token.
- On older versions, GitLab sends a secret token with each delivery, and you paste the secret token.

If you'd rather use a signing token GitLab generated, paste that into the card before you create the secret, and Loomplane verifies deliveries against it.

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
