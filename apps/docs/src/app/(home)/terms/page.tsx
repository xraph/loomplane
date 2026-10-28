import type { Metadata } from "next";
import Link from "next/link";
import { Mail, operator, PolicyPage } from "@/components/landing/policy";
import { githubUrl } from "@/components/landing/site";

export const metadata: Metadata = {
  title: "Terms of use",
  description:
    "The terms for the Loomplane app, the hosted service at app.loomplane.com and this website.",
};

export default function TermsPage() {
  return (
    <PolicyPage
      id="terms-title"
      crumb="Terms"
      title="Terms of use,"
      thread="for the app and the service."
      lede={
        <>
          These terms apply when you use the Loomplane app, the hosted service
          at <code>app.loomplane.com</code> or this website. By using any of
          them, you agree to these terms and to our{" "}
          <Link href="/privacy">privacy policy</Link>.
        </>
      }
      updated="October 28, 2026"
    >
      <h2>Who you're agreeing with</h2>
      <p>
        {operator} is run by Rex Raphael, an independent developer. In these
        terms, "we" and "us" mean {operator}, and "you" means you, or the
        company you use Loomplane for. If you accept these terms for a company,
        you're confirming that you're allowed to.
      </p>

      <h2>Servers you host yourself</h2>
      <p>
        Loomplane's server is source-available under its own licence, and anyone
        can run it. When you connect the app to a server someone else runs,
        these terms cover the app. Whoever runs that server sets the terms for
        using it, and we aren't responsible for it.
      </p>

      <h2>Your account</h2>
      <p>
        You need to be at least 16 to create an account. Give us accurate
        details, keep your password and API keys to yourself, and tell us at{" "}
        <Mail /> if you think someone else has used your account. You're
        responsible for what happens under your account and under the API keys
        you create.
      </p>

      <h2>Your clouds and your bills</h2>
      <p>
        Loomplane changes real infrastructure. When you or someone in your
        workspace approves a plan, it creates, changes or removes resources in
        the cloud accounts you connected, using the access you gave it. Read
        plans before you approve them. You're responsible for the charges your
        cloud providers bill you, including for anything Loomplane creates on
        your behalf, and for following their terms.
      </p>

      <h2>What's yours</h2>
      <p>
        Your code, manifests, configuration, secrets and everything else you put
        into Loomplane stay yours. You give us permission to store, process and
        send them only as far as we need to run the service for you. You can
        delete your account at any time; the{" "}
        <Link href="/delete-account">account deletion page</Link> says how.
      </p>

      <h2>Using it fairly</h2>
      <p>You agree not to:</p>
      <ul>
        <li>
          use Loomplane to break the law or to deploy anything that harms
          others, like malware or phishing pages;
        </li>
        <li>
          get into accounts, workspaces or systems you aren't allowed into, or
          probe, scan or load-test the hosted service without our written
          permission;
        </li>
        <li>
          get around usage limits, or resell the hosted service without our
          agreement;
        </li>
        <li>
          copy, modify or reverse-engineer the app, except where the law lets
          you.
        </li>
      </ul>
      <p>
        Reporting a security problem privately, through{" "}
        <a href={`${githubUrl}/security/advisories/new`}>
          GitHub's private vulnerability reporting
        </a>
        , isn't a breach of these terms, and we're glad to get the report.
      </p>

      <h2>The app</h2>
      <p>
        We give you a personal, non-exclusive, non-transferable licence to use
        the Loomplane app on devices you own or control, under these terms and
        the rules of the store you got it from. The app is free. Its code is
        ours and isn't open source, though the protocol it speaks is public.
      </p>
      <p>
        If you got the app from Apple's App Store, Apple isn't a party to these
        terms and isn't responsible for the app or for supporting it. Apple and
        its subsidiaries can still enforce these terms against you as
        third-party beneficiaries. The same goes for Google if you got the app
        from Google Play.
      </p>

      <h2>Changes and availability</h2>
      <p>
        We're improving Loomplane all the time, so features will change, and
        some may go away. We aim to keep the hosted service running, but we
        can't promise it'll never be down, and we may pause it for maintenance.
      </p>

      <h2>No warranty</h2>
      <p>
        Loomplane, the app and the hosted service are provided "as is" and "as
        available", without warranties of any kind, whether express or implied,
        including warranties of merchantability, fitness for a particular
        purpose and non-infringement. We don't promise that they'll be
        error-free, or that a deployment or rollback will succeed.
      </p>

      <h2>Limits on liability</h2>
      <p>
        To the extent the law allows, we aren't liable for indirect, incidental,
        special, consequential or punitive damages, or for lost profits,
        revenue, data or cloud charges, arising from your use of Loomplane. Our
        total liability for any claim about Loomplane is limited to the greater
        of what you paid us in the 12 months before the claim and 100 US
        dollars. Some places don't allow these limits, so they may not all apply
        to you.
      </p>
      <p>
        You agree to cover us against claims by others that come from your
        content or from your breaking these terms.
      </p>

      <h2>Ending this</h2>
      <p>
        You can stop using Loomplane and delete your account whenever you like.
        We may suspend or close an account that breaks these terms or puts the
        service or other people at risk. When we can, we'll warn you first and
        give you a chance to export your data. The sections on what's yours,
        warranties, liability and the law that applies continue after an account
        closes.
      </p>

      <h2>The law that applies</h2>
      <p>
        These terms are governed by the laws of the State of Delaware, United
        States, apart from its conflict-of-law rules. Disputes go to the state
        or federal courts in Delaware, and you and we agree to their
        jurisdiction. If you're a consumer, this doesn't take away any
        protection the law where you live gives you.
      </p>

      <h2>Changes to these terms</h2>
      <p>
        When these terms change, we'll update them here and change the date at
        the top. For changes that matter, we'll tell you by email or in the app
        before they take effect. Using Loomplane after that means you accept the
        new terms. If any part turns out to be unenforceable, the rest still
        applies.
      </p>

      <h2>Contact</h2>
      <p>
        Questions about these terms go to <Mail />.
      </p>
    </PolicyPage>
  );
}
