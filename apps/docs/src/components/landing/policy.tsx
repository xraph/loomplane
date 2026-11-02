import Link from "next/link";
import type { ReactNode } from "react";

// Who publishes the app and runs the hosted service, as the policies name them.
export const operator = "XRaph";
export const contactEmail = "rex@xraph.com";

// The shell the privacy policy, terms and account deletion pages share: a
// short hero, then the text at a reading width.
export function PolicyPage({
  id,
  crumb,
  title,
  thread,
  lede,
  updated,
  children,
}: {
  id: string;
  crumb: string;
  title: string;
  thread: string;
  lede: ReactNode;
  updated: string;
  children: ReactNode;
}) {
  return (
    <>
      <section className="page-hero grain" aria-labelledby={id}>
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>{crumb}</b>
          </div>
          <h1 id={id}>
            {title} <span className="thread">{thread}</span>
          </h1>
          <p className="lede">{lede}</p>
        </div>
      </section>
      <div className="wrap">
        <article className="policy">
          <p className="updated">Last updated {updated}</p>
          {children}
        </article>
      </div>
    </>
  );
}

export function Mail() {
  return <a href={`mailto:${contactEmail}`}>{contactEmail}</a>;
}
