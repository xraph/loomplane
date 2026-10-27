import "@/components/landing/landing.css";
import { Effects } from "@/components/landing/effects";
import { FinalCta } from "@/components/landing/final-cta";
import { Footer } from "@/components/landing/footer";
import { Nav } from "@/components/landing/nav";
import { Silk } from "@/components/landing/silk";

// The marketing pages share one shell: nav, the hero light, a closing call to action and the footer.
export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <div className="lp">
      <Nav />
      <main id="top">
        <Silk />
        <div className="page">{children}</div>
        <FinalCta />
      </main>
      <Footer />
      <Effects />
    </div>
  );
}
