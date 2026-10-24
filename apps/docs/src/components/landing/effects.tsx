"use client";

import { usePathname } from "next/navigation";
import { useEffect } from "react";

const REVEAL =
  ".feature, .cell, .stat, .engine, .table-wrap, .faq details, .lang, .sec h2, .sec-lede, .flow li, .ide-wrap";

/**
 * Page-level motion for the landing pages: sections below the first screen
 * ease in, terminals type their lines in, feature cards lean toward the
 * pointer, and the subnav buttons scroll to their section.
 */
export function Effects() {
  const pathname = usePathname();

  // biome-ignore lint/correctness/useExhaustiveDependencies: re-runs for each page
  useEffect(() => {
    const root = document.querySelector(".lp main");
    if (!root) return;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const cleanups: Array<() => void> = [];

    // a blinking caret ends every terminal
    for (const pre of root.querySelectorAll(".vis-term pre")) {
      const last = pre.lastElementChild;
      if (last && !last.querySelector(".caret"))
        last.insertAdjacentHTML(
          "beforeend",
          '<span class="caret" aria-hidden="true"></span>',
        );
    }

    if (!still && "IntersectionObserver" in window) {
      const reveal = new IntersectionObserver(
        (es) => {
          for (const e of es) {
            if (!e.isIntersecting) continue;
            e.target.classList.add("reveal-in");
            e.target.classList.remove("reveal-pre");
            reveal.unobserve(e.target);
          }
        },
        { rootMargin: "0px 0px -8% 0px" },
      );
      root.querySelectorAll<HTMLElement>(REVEAL).forEach((el, k) => {
        if (
          el.classList.contains("reveal-in") ||
          el.getBoundingClientRect().top < window.innerHeight
        )
          return;
        el.classList.add("reveal-pre");
        el.style.transitionDelay = `${(k % 4) * 70}ms`;
        reveal.observe(el);
      });

      const typing = new IntersectionObserver(
        (es) => {
          for (const e of es) {
            if (!e.isIntersecting) continue;
            e.target.classList.add("typing");
            typing.unobserve(e.target);
          }
        },
        { rootMargin: "0px 0px -10% 0px" },
      );
      for (const t of root.querySelectorAll(".vis-term")) {
        if (t.getBoundingClientRect().top > window.innerHeight)
          typing.observe(t);
      }
      cleanups.push(
        () => reveal.disconnect(),
        () => typing.disconnect(),
      );
    }

    if (!still && matchMedia("(hover: hover)").matches) {
      for (const v of root.querySelectorAll<HTMLElement>(".vis")) {
        const main = v.querySelector<HTMLElement>(".vis-main");
        if (!main) continue;
        const move = (e: PointerEvent) => {
          const r = main.getBoundingClientRect();
          const x = (e.clientX - r.left) / r.width;
          const y = (e.clientY - r.top) / r.height;
          main.style.setProperty("--mx", `${(x * 100).toFixed(1)}%`);
          main.style.setProperty("--my", `${(y * 100).toFixed(1)}%`);
          main.style.transform = `perspective(1400px) rotateX(${((0.5 - y) * 4).toFixed(2)}deg) rotateY(${((x - 0.5) * 5).toFixed(2)}deg)`;
        };
        const leave = () => {
          main.style.transform = "";
        };
        v.addEventListener("pointermove", move);
        v.addEventListener("pointerleave", leave);
        cleanups.push(() => {
          v.removeEventListener("pointermove", move);
          v.removeEventListener("pointerleave", leave);
        });
      }
    }

    for (const b of root.querySelectorAll<HTMLElement>("[data-jump]")) {
      const jump = () =>
        document.getElementById(b.dataset.jump ?? "")?.scrollIntoView({
          behavior: still ? "auto" : "smooth",
          block: "start",
        });
      b.addEventListener("click", jump);
      cleanups.push(() => b.removeEventListener("click", jump));
    }

    return () => {
      for (const c of cleanups) c();
    };
  }, [pathname]);

  return null;
}
