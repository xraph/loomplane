import type { Metadata } from "next";
import Link from "next/link";
import { Mail, operator, PolicyPage } from "@/components/landing/policy";

export const metadata: Metadata = {
  title: "Privacy policy",
  description:
    "What the Loomplane app and the hosted service keep about you, where it goes, and how to get it deleted.",
};

export default function PrivacyPage() {
  return (
    <PolicyPage
      id="privacy-title"
      crumb="Privacy"
      title="Your data,"
      thread="and where it goes."
      lede={
        <>
          There are no ads, no analytics and no tracking in the Loomplane app.
          This page says what it does keep, where that lives, and how you get it
          removed.
        </>
      }
      updated="October 29, 2026"
    >
      <h2>Who this covers</h2>
      <p>
        {operator} is run by Rex Raphael, an independent developer, and
        publishes the Loomplane app for iOS and Android. {operator} also runs
        Loomplane's hosted service at <code>app.loomplane.com</code> and this
        website, <code>loomplane.dev</code>. In this policy, "we" and "us" mean{" "}
        {operator}.
      </p>
      <p>
        Loomplane is also software you can run yourself. When you connect the
        app to a Loomplane that your company or someone else hosts, that server
        belongs to them. Apart from push notifications, which go through our
        gateway (see <em>Push notifications</em> below), we never see what
        passes between your phone and that server, and their own privacy policy
        covers what they keep. Ask whoever runs it.
      </p>

      <h2>What stays on your phone</h2>
      <p>
        The app keeps a few things so you don't have to sign in every time you
        open it. All of it sits in the platform's secure storage: the Keychain
        on iOS, and storage encrypted with the Android Keystore on Android.
      </p>
      <ul>
        <li>
          The address of each Loomplane server you sign in to, and the public
          key it gives the app.
        </li>
        <li>
          Your session token for each of those servers, the email address you
          signed in with, and the workspace you last used.
        </li>
        <li>
          Your settings for this phone: theme, accent colour, which notices you
          want, quiet hours, and whether production actions ask for Face ID or
          your fingerprint.
        </li>
      </ul>
      <p>
        Signing out of a server removes its session from your phone. Deleting
        the app removes the rest on Android. iOS can keep Keychain items after
        an app is deleted, so sign out first if you want them gone straight
        away.
      </p>
      <p>
        When the app asks for Face ID, Touch ID or your fingerprint, your phone
        does the check. The app only learns whether it passed. Your face and
        fingerprints never reach the app or us.
      </p>

      <h2>What leaves your phone</h2>
      <p>
        The app talks to the Loomplane server you choose, over HTTPS (plain HTTP
        only for a server on your own network). It sends your sign-in details
        and session token, and the actions you take, like approving a plan or
        marking a notification read. It shows you what that server sends back.
      </p>
      <p>
        The first time it opens, the app downloads its typeface, Geist, from
        Google Fonts. Google receives your IP address for that request, as for
        any web request, and{" "}
        <a href="https://policies.google.com/privacy">
          Google's privacy policy
        </a>{" "}
        covers it.
      </p>
      <p>
        Some screens open a page in your browser, on your server's dashboard or
        on this site. Your browser handles those like any other web page.
      </p>
      <p>
        The app doesn't contain advertising, analytics or crash-reporting
        services, and it doesn't use your location, contacts, photos, camera or
        microphone.
      </p>

      <h2>Push notifications</h2>
      <p>
        If you turn notifications on, the app asks your phone for a push token
        (from Apple on iOS, from Google's Firebase Cloud Messaging on Android)
        and makes a key pair. The private key never leaves your phone. The app
        registers with the push gateway your server names, which is ours at{" "}
        <code>push.loomplane.com</code> unless whoever runs the server changed
        it. The gateway receives the token and a fingerprint of the Loomplane
        server's key (not the key itself), so it knows which server each phone
        belongs to. On iPhone it also learns whether it's a test build. It seals
        those into a handle and keeps nothing. The app then gives the server
        you're signed in to that handle, along with the public key.
      </p>
      <p>
        That server keeps a record of the phone, tied to your account on it: the
        public key, the gateway handle, the platform, a name for the phone
        (something like "Pixel 9"), the time zone, your quiet hours, your
        notification settings, and when the phone was added and last seen. When
        you sign out on that phone, the app asks the server to delete the
        record. Deleting your account deletes it too, and records for phones
        that can no longer be reached are removed as well. The server also keeps
        only the 20 phones you used most recently.
      </p>
      <p>
        When the server has something for you, it encrypts the notification to
        your phone's public key before it sends it. The gateway gets the
        encrypted payload, the handle (so only the gateway can open the push
        token inside it), how urgent the message is, what kind it is, how long
        the push may wait to be delivered, a collapse ID that lets a newer
        notification replace an older one, a key ID for your phone (a stable
        fingerprint of its public key, sent with every push), and which server
        key signed the request. It can't read the title, the text, your
        workspace or the buttons. Apple or Google deliver the message to your
        phone, and your phone decrypts it. Apple or Google can see that a
        notification arrived and when, but not what it says.
      </p>
      <p>
        The gateway doesn't store any of this itself. Its logs keep a
        fingerprint of the server's key, the platform, the urgency, the kind,
        whether delivery worked and how long it took.
      </p>
      <p>
        If you run Loomplane yourself, you can turn push off with{" "}
        <code>LOOMPLANE_PUSH_ENABLED</code> or point the server at a gateway of
        your own, though that only works with an app build you publish yourself,
        because only our gateway reaches the store version of the app. By
        default a self-hosted or company-run server sends through our gateway,
        and the same limits apply: we can't see what it says.
      </p>

      <h2>What the hosted service keeps</h2>
      <p>
        If you use Loomplane at <code>app.loomplane.com</code>, from the app or
        the web, we keep what it needs to work:
      </p>
      <ul>
        <li>
          Your name, email address, a hash of your password (never the password
          itself), and any passkeys or sign-in methods you add.
        </li>
        <li>
          The organizations you belong to, their members and roles, and their
          projects, manifests, plans, deployments, variables and secrets, along
          with the cloud accounts and repositories you connect.
        </li>
        <li>
          An audit trail of changes (who, what and when), deployment logs, and
          notifications.
        </li>
        <li>
          IP addresses, browser or app version, and sign-in times, which we use
          to keep accounts safe and stop abuse.
        </li>
      </ul>
      <p>
        Secrets and cloud credentials are encrypted before they're stored. The{" "}
        <Link href="/security">security page</Link> explains how.
      </p>

      <h2>How we use it</h2>
      <p>
        To run the service you asked for: sign you in, show you your projects,
        and carry out the plans and deployments you approve. To send the emails
        you've chosen to get, such as deployment notices and invitations. To
        keep the service secure and fix what breaks. We don't sell your data,
        and we don't use it for advertising.
      </p>

      <h2>Who else handles it</h2>
      <p>
        We use a small number of providers to run the hosted service, such as
        the cloud company that hosts it and the service that delivers its email.
        They process data only to provide those services to us.
      </p>
      <p>
        When you connect a cloud account, a repository host or another
        integration, Loomplane sends that provider what's needed to do what you
        asked, like the configuration for a deployment you approved. Their own
        terms cover what they do with it.
      </p>
      <p>
        We'll disclose data if the law requires it, and we'll tell you first
        when the law allows.
      </p>

      <h2>How long we keep it</h2>
      <p>
        Your account data stays while your account is open. When you delete your
        account, your name, email address, password and sign-in methods are
        erased at once, and your sessions and API keys stop working. Backups
        that still hold a copy expire within 30 days. Workspaces and their
        projects belong to the organization, so they stay for its other members,
        and their member lists and audit trails show an anonymous ID where your
        name used to be. A record of the terms and consents you accepted is kept
        as proof of what you agreed to.
      </p>
      <p>
        The <Link href="/delete-account">account deletion page</Link> has the
        steps.
      </p>

      <h2>Your rights</h2>
      <p>
        Wherever you live, you can ask us for a copy of the data we hold about
        you, ask us to correct it, or ask us to delete it. Depending on where
        you live (the EU, the UK and California, for example), the law may give
        you further rights, such as objecting to how we use your data or
        complaining to your data protection authority. Email <Mail /> from the
        address on your account and we'll answer within 30 days.
      </p>

      <h2>Where it's stored</h2>
      <p>
        The hosted service runs in the United States. If you use it from
        somewhere else, your data is transferred there, with the protections
        this policy describes.
      </p>

      <h2>Children</h2>
      <p>
        Loomplane is a tool for software teams and isn't meant for children. You
        need to be at least 16 to create an account. If you think a child has
        given us data, email us and we'll delete it.
      </p>

      <h2>Changes</h2>
      <p>
        When this policy changes, we'll update it here and change the date at
        the top. If a change matters to how your data is used, we'll also tell
        you by email or in the app before it takes effect.
      </p>

      <h2>Contact</h2>
      <p>
        Questions or requests about your data go to <Mail />.
      </p>
    </PolicyPage>
  );
}
