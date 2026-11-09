import type { Metadata } from "next";
import Link from "next/link";
import { Mail, operator, PolicyPage } from "@/components/landing/policy";

export const metadata: Metadata = {
  title: "Delete your account",
  description:
    "How to delete your Loomplane account, from the app or by email, and what's removed.",
};

export default function DeleteAccountPage() {
  return (
    <PolicyPage
      id="delete-title"
      crumb="Delete your account"
      title="Delete your account,"
      thread="from the app or by email."
      lede={
        <>
          This page covers accounts on Loomplane's hosted service, used through
          the Loomplane app for iOS and Android by {operator}, or on the web at{" "}
          <code>app.loomplane.com</code>.
        </>
      }
      updated="October 28, 2026"
    >
      <h2>From the app</h2>
      <ol>
        <li>
          Open Loomplane and go to the <b>More</b> tab.
        </li>
        <li>
          Tap your name at the top to open <b>Account</b>.
        </li>
        <li>
          Tap <b>Delete account</b>, read the summary, and confirm. If you use
          Face ID or a fingerprint for production actions, the app asks for it.
        </li>
      </ol>
      <p>
        The account is deleted straight away and the app signs you out of that
        server. Other servers you're signed in to on the phone aren't touched.
      </p>

      <h2>Without the app</h2>
      <p>
        Email <Mail /> from the address on your account, with "Delete my
        account" in the subject. We'll delete it within 30 days and reply to
        confirm. If you write from a different address, we'll ask you to prove
        the account is yours first.
      </p>

      <h2>What's deleted</h2>
      <ul>
        <li>
          Your name, email address, username, profile picture and phone number.
        </li>
        <li>
          Your password, passkeys, two-factor setup and the Google, GitHub or
          other accounts you signed in with.
        </li>
        <li>
          Every session and API key you created. They stop working at once, on
          every device.
        </li>
      </ul>
      <p>Backups that still hold a copy of these expire within 30 days.</p>

      <h2>What's kept</h2>
      <p>
        Workspaces belong to their organization, not to one person. Their
        projects, deployments, variables and connected cloud accounts stay for
        the other members. Your place in their member lists and audit trails
        stays, under an anonymous ID in place of your name, so a workspace can
        still see what changed; an admin can remove the member entry. We also
        keep a record of the terms and consents you accepted, as proof of what
        you agreed to. If you're the only owner of a workspace and want it gone
        too, delete it from the dashboard before you delete your account, or say
        so in your email.
      </p>
      <p>
        Deleting your account doesn't remove resources Loomplane created in your
        cloud accounts. They're yours, and you remove them with your cloud
        provider.
      </p>

      <h2>Accounts on other servers</h2>
      <p>
        If your company runs its own Loomplane, your account lives on its
        server, not ours. You can still use <b>Delete account</b> in the app
        while you're signed in to it, or ask whoever runs that server.
      </p>
      <p>
        The <Link href="/privacy">privacy policy</Link> has more on what we keep
        and why.
      </p>
    </PolicyPage>
  );
}
