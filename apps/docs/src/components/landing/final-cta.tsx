import Link from "next/link";
import { CopyButton } from "./copy-button";
import { installCommands, installGuideHref } from "./install-commands";
import { LoomMark } from "./loom-mark";
import { cloudWaitlistUrl } from "./site";

/** The closing section on every landing page. */
export function FinalCta() {
  return (
    <section className="final paper" aria-labelledby="final-title">
      <LoomMark />
      <div className="wrap sec">
        <div className="eyebrow">Cloud or self-hosted</div>
        <h2 id="final-title">
          Deploy your monorepo to your own clouds this afternoon.
        </h2>
        <p className="sec-lede">
          Open source and self-hosted, so you can run it today. Loomplane Cloud
          is coming for teams who&apos;d rather we ran it. Your applications
          don&apos;t need Forge, or Go.
        </p>
        <div className="cta-rows">
          <a className="cta-row cta-row-cloud" href={cloudWaitlistUrl}>
            <span>
              Join the Loomplane Cloud waitlist
              <small>We email you when your workspace is ready.</small>
            </span>
            <span className="arr" aria-hidden="true">
              ↗
            </span>
          </a>
          {installCommands.map((c) => (
            <div className="cta-row" key={c.id}>
              <span className="p">$</span>
              <code id={`final-${c.id}`}>{c.text}</code>
              <span className="kind">{c.kind}</span>
              <CopyButton
                text={c.text}
                label={c.label}
                targetId={`final-${c.id}`}
              />
            </div>
          ))}
          <Link className="cta-row" href={installGuideHref}>
            Read the install guide{" "}
            <span className="arr" aria-hidden="true">
              ↗
            </span>
          </Link>
        </div>
      </div>
    </section>
  );
}
