# Bitbucket

Loomplane reads public Bitbucket Cloud repositories with no setup. A source is written `bitbucket.org/<workspace>/<repo>`, and you can paste the repository's URL wherever Loomplane asks for one. To read private repositories, you create an OAuth consumer once for the whole instance, and your organization's owners and admins connect their workspaces to it. Bitbucket Data Center and Server aren't supported.

## Create the OAuth consumer

In Bitbucket, open a workspace's **Settings** and choose **OAuth consumers**, then **Add consumer**. It can live under any workspace, and it doesn't need to be a workspace your organization will connect. Fill in:

| Setting | Value |
|---|---|
| Name | Anything, such as Loomplane |
| Callback URL | `<public_url>/integrations/bitbucket/callback` |
| Permissions: Account | **Read** |
| Permissions: Repositories | **Read** |
| Permissions: Webhooks | **Read and write** |
| Everything else | No access |

Save it, then open the consumer to copy its **Key** and **Secret**.

Account Read is how Loomplane learns who signed in and checks they belong to the workspace. Repositories Read is the only access to code. Webhooks is for push events, which the Webhooks section covers. Loomplane never asks for write access to your code.

## Configure Loomplane

Set both variables for `loomplane` and `loomplane-worker`, and keep the secret out of any file you commit:

```bash
LOOMPLANE_BITBUCKET_CLIENT_ID=<the consumer's key>
LOOMPLANE_BITBUCKET_CLIENT_SECRET=<the consumer's secret>
```

In `config.yaml` they're `bitbucket.client_id` and `bitbucket.client_secret`. Set both or neither. Loomplane refuses to start with only one. With neither, Bitbucket keeps working for public repositories, and connecting a workspace is refused with a message saying there's no consumer.

`bitbucket.api_url` and `bitbucket.web_url` default to Bitbucket Cloud's (`https://api.bitbucket.org/2.0` and `https://bitbucket.org`). You'd change them only to point at a test double.

## Connect a workspace

An owner or admin opens **Integrations**, finds the Bitbucket card, types the workspace's slug (the `acme` in `bitbucket.org/acme`) and chooses **Connect workspace**. Bitbucket asks them to approve the consumer, and sends them back to Loomplane. If they've approved it before, Bitbucket skips the question.

Each workspace is its own connection, so connecting a second one repeats the steps. The same workspace can be connected by several organizations, and each one gets a sign-in of its own.

Once it's connected, any project in the organization whose repository sits in that workspace is analyzed with access to it, and the repository picker lists the workspace's repositories next to your GitHub accounts.

## What a Bitbucket token can read

This is the one place Bitbucket is looser than GitHub, so it's worth knowing before you roll it out. A GitHub installation token reads one repository. A Bitbucket OAuth token reads every repository the connecting person can read, in every workspace they belong to. Bitbucket's consumer permissions say what kind of access the token has (read repositories), not which repositories it covers.

We narrow it on our side:

- A token is only used for a repository in the workspace its connection names. A connection to `acme` is never used for `other/repo`, even if the person who connected it can read that too.
- Only the organization's own connections are used, so one organization can't read code through another's.
- A connection that needs connecting again is never used. The repository is read anonymously instead, which works for a public one and fails for a private one.
- When someone connects, Loomplane checks that their Bitbucket account is a member of the workspace they typed. If it isn't, the sign-in is dropped and nothing is kept.
- Tokens never appear in an API response, a log line, an audit record or an error message.

Access tokens live in memory only and expire within a couple of hours. The one long-lived secret is the refresh token, which is encrypted in Vault under a key of its own, derived from your `secret_key`. Disconnecting deletes it.

If you want a tighter boundary than that, connect a workspace using a Bitbucket account that can read only the repositories you want Loomplane to see.

## When a workspace needs connecting again

The card shows **Reconnect needed**, with the reason, when:

- Bitbucket stops accepting the stored sign-in, because the person revoked the consumer or the refresh token lapsed.
- The member who connected the workspace has left your Loomplane organization.
- The Bitbucket account that connected it is no longer in the workspace.
- Loomplane can't read the stored sign-in from Vault.

Anyone who may connect workspaces can fix it by connecting the same workspace again. It keeps its place, and the new sign-in replaces the old one.

## Webhooks

When you attach a Bitbucket repository to a project, Loomplane adds a webhook to it, so a push to the default branch re-analyzes the project without anyone pressing Analyze. Your organization gets one webhook per repository. A second project on the same repository shares it, and it's removed when the organization's last source on that repository goes.

The webhook only goes in when three things are true:

- Your organization has connected the workspace the repository is in, and the connection is ready.
- The person who connected the workspace is an admin of the repository. Bitbucket only lets a repository admin add a webhook, which is also why the consumer asks for Webhooks Read and write.
- Your `public_url` is https. Bitbucket won't deliver to plain http, so a local instance on `http://localhost:8780` never gets one.

If any of them isn't, the source still attaches and Analyze works as it always has. The Sources list shows **Push re-analysis is off** beside the source, with the reason. Fix what the reason names (connect the workspace, have a repository admin connect it again, or move to an https address), then choose **Turn on**. Loomplane tries to add the webhook again and keeps it if Bitbucket agrees. You need the same role to turn it on as to connect a workspace.

Only pushes to the repository's default branch re-analyze, the same as with GitHub. A source with no branch set follows the default, and a source that names a branch is re-analyzed only while that branch is the default. A push to any other branch, a tag or a deleted branch starts nothing. Loomplane asks Bitbucket which branch is the default each time a push arrives, so if you rename it, the next push already uses the new name.

Each webhook has a secret of its own, derived from your `secret_key` and never stored, and Loomplane checks the signature on every delivery. A delivery only re-analyzes projects in the organization that owns the webhook, even when another organization has projects on the same repository.

Disconnecting a workspace removes every webhook it added, on Bitbucket and in Loomplane. If Bitbucket refuses to remove one (say the account lost admin on the repository), Loomplane stops listening to it anyway and its deliveries get a 404. You can delete it yourself on Bitbucket, under the repository's settings.

## Revoking access

Disconnecting a workspace on the Integrations card removes it from the organization and deletes its refresh token. It doesn't un-approve the consumer on Bitbucket. To do that, the person who connected opens their Bitbucket **Personal settings**, finds Loomplane among the apps they've authorized, and revokes it there.

To cut everything off at once, delete the consumer on Bitbucket, or change its secret and update `LOOMPLANE_BITBUCKET_CLIENT_SECRET`. Every connection then shows Reconnect needed, and private repositories stop being readable until the workspaces are connected again.
