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

Account Read is how Loomplane learns who signed in and checks they belong to the workspace. Repositories Read is the only access to code. Webhooks is for push events, which the next section covers. Loomplane never asks for write access to your code.

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

Push re-analysis for Bitbucket repositories is coming in the next change, and this section will cover how Loomplane adds the webhook, why it needs repository admin, and what to do when it can't. Until then, press Analyze to pick up new commits.

## Revoking access

Disconnecting a workspace on the Integrations card removes it from the organization and deletes its refresh token. It doesn't un-approve the consumer on Bitbucket. To do that, the person who connected opens their Bitbucket **Personal settings**, finds Loomplane among the apps they've authorized, and revokes it there.

To cut everything off at once, delete the consumer on Bitbucket, or change its secret and update `LOOMPLANE_BITBUCKET_CLIENT_SECRET`. Every connection then shows Reconnect needed, and private repositories stop being readable until the workspaces are connected again.
