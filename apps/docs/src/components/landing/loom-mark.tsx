'use client';

import { useEffect, useRef } from 'react';

const IVORY = 'M0 10.5L0 21A11 11 0 0 0 11 32L28 32A4 4 0 0 0 28 24L11 24A3 3 0 0 1 8 21L8 10.5Z';
const BLUE = 'M32 21.5L32 11A11 11 0 0 0 21 0L4 0A4 4 0 0 0 4 8L21 8A3 3 0 0 1 24 11L24 21.5Z';
const SLICES = 9;

function Thread({
  id,
  d,
  face,
  sheenId,
  sheen,
  from,
  to,
}: {
  id: string;
  d: string;
  face: string;
  sheenId: string;
  sheen: string;
  from: string;
  to: string;
}) {
  return (
    <div className="lm-thread" id={id}>
      {Array.from({ length: SLICES }, (_, i) => SLICES - i).map((q) => (
        <svg
          key={q}
          className="lm-layer"
          viewBox="0 0 32 32"
          style={{ transform: `translateZ(${(-q * 1.6).toFixed(1)}px)` }}
          aria-hidden="true"
        >
          <path style={{ fill: `color-mix(in oklch, ${from} ${Math.round((q / SLICES) * 100)}%, ${to})` }} d={d} />
        </svg>
      ))}
      <svg className="lm-layer lm-face" viewBox="0 0 32 32" aria-hidden="true">
        <defs>
          <radialGradient id={sheenId} gradientUnits="userSpaceOnUse" cx="10" cy="8" r="14">
            <stop offset="0" stopColor={sheen} stopOpacity=".85" />
            <stop offset=".5" stopColor={sheen} stopOpacity=".18" />
            <stop offset="1" stopColor={sheen} stopOpacity="0" />
          </radialGradient>
        </defs>
        <path style={{ fill: face }} d={d} />
        <path className="lm-sheen" fill={`url(#${sheenId})`} d={d} />
      </svg>
    </div>
  );
}

/**
 * The Loomplane mark as an object: it tilts toward the cursor, catches light,
 * pulls apart into its two threads when dragged, and spins when clicked.
 */
export function LoomMark() {
  const markRef = useRef<HTMLDivElement>(null);
  const rigRef = useRef<HTMLDivElement>(null);
  const cursorRef = useRef<HTMLDivElement>(null);
  const cursorText = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const mark = markRef.current;
    const rig = rigRef.current;
    const cursor = cursorRef.current;
    const label = cursorText.current;
    const final = mark?.closest('.final') as HTMLElement | null;
    const ivory = mark?.querySelector<HTMLElement>('#lm-ivory');
    const blue = mark?.querySelector<HTMLElement>('#lm-blue');
    if (!mark || !rig || !cursor || !label || !final || !ivory || !blue) return;
    const sheens = mark.querySelectorAll('radialGradient');

    const S = { rx: 0, ry: 0, trx: 0, try: 0, spin: 0, spinV: 0, bx: 0, by: 0, bvx: 0, bvy: 0, ix: 0, iy: 0, ivx: 0, ivy: 0, sx: 10, sy: 8 };
    let drag: { x: number; y: number; dx: number; dy: number; moved: boolean } | null = null;
    let hover = false;
    let lastMove = 0;
    let visible = false;
    let raf = 0;
    const t0 = performance.now();

    const apply = () => {
      rig.style.transform = `rotateX(${S.rx.toFixed(2)}deg) rotateY(${(S.ry + S.spin).toFixed(2)}deg)`;
      const sep = Math.hypot(S.bx - S.ix, S.by - S.iy);
      ivory.style.transform = `translate3d(${S.ix.toFixed(1)}px,${S.iy.toFixed(1)}px,${(-10 - sep * 0.15).toFixed(1)}px)`;
      blue.style.transform = `translate3d(${S.bx.toFixed(1)}px,${S.by.toFixed(1)}px,${(34 + sep * 0.35).toFixed(1)}px)`;
      mark.style.setProperty('--tension', Math.min(sep / 160, 1).toFixed(3));
      for (const g of sheens) {
        g.setAttribute('cx', S.sx.toFixed(2));
        g.setAttribute('cy', S.sy.toFixed(2));
      }
    };

    if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
      S.rx = 8;
      S.ry = -14;
      apply();
      return;
    }

    const tick = (now: number) => {
      raf = 0;
      if (!visible) return;
      if (now - lastMove > 1800 && !drag) {
        const k = (now - t0) / 1000;
        S.trx = Math.sin(k * 0.7) * 9;
        S.try = Math.cos(k * 0.5) * 16;
      }
      S.rx += (S.trx - S.rx) * 0.07;
      S.ry += (S.try - S.ry) * 0.07;
      S.spinV *= 0.955;
      S.spin += S.spinV;
      if (!drag && Math.abs(S.spinV) < 0.05) S.spin += (Math.round(S.spin / 360) * 360 - S.spin) * 0.06;
      if (drag) {
        S.bx += (drag.dx * 0.62 - S.bx) * 0.25;
        S.by += (drag.dy * 0.62 - S.by) * 0.25;
        S.ix += (-drag.dx * 0.22 - S.ix) * 0.25;
        S.iy += (-drag.dy * 0.22 - S.iy) * 0.25;
      } else {
        S.bvx = (S.bvx - S.bx * 0.09) * 0.84;
        S.bvy = (S.bvy - S.by * 0.09) * 0.84;
        S.bx += S.bvx;
        S.by += S.bvy;
        S.ivx = (S.ivx - S.ix * 0.09) * 0.84;
        S.ivy = (S.ivy - S.iy * 0.09) * 0.84;
        S.ix += S.ivx;
        S.iy += S.ivy;
      }
      apply();
      raf = requestAnimationFrame(tick);
    };
    const wake = () => {
      if (!raf && visible) raf = requestAnimationFrame(tick);
    };

    const onMove = (e: PointerEvent) => {
      const r = mark.getBoundingClientRect();
      const cx = Math.max(-1.5, Math.min(2.5, (e.clientX - r.left) / r.width));
      const cy = Math.max(-1.5, Math.min(2.5, (e.clientY - r.top) / r.height));
      S.trx = (0.5 - cy) * 34;
      S.try = (cx - 0.5) * 42;
      S.sx = cx * 32;
      S.sy = cy * 32;
      lastMove = performance.now();
      const fr = final.getBoundingClientRect();
      cursor.style.transform = `translate(${(e.clientX - fr.left).toFixed(0)}px,${(e.clientY - fr.top).toFixed(0)}px)`;
      if (drag) {
        drag.dx = e.clientX - drag.x;
        drag.dy = e.clientY - drag.y;
        drag.moved = drag.moved || Math.hypot(drag.dx, drag.dy) > 6;
        label.textContent = Math.hypot(drag.dx, drag.dy) > 140 ? 'let go' : 'pulling';
      }
      wake();
    };
    const onEnter = () => {
      hover = true;
      cursor.classList.add('on');
    };
    const onLeave = () => {
      hover = false;
      if (!drag) cursor.classList.remove('on');
    };
    const onDown = (e: PointerEvent) => {
      drag = { x: e.clientX, y: e.clientY, dx: 0, dy: 0, moved: false };
      try {
        mark.setPointerCapture(e.pointerId);
      } catch {
        // a synthetic or already released pointer cannot be captured
      }
      cursor.classList.add('grab');
      label.textContent = 'pulling';
      wake();
    };
    const release = () => {
      if (!drag) return;
      if (!drag.moved) S.spinV += 26 * (S.ry >= 0 ? 1 : -1);
      else {
        S.bvx = drag.dx * 0.05;
        S.bvy = drag.dy * 0.05;
      }
      drag = null;
      cursor.classList.remove('grab');
      label.textContent = 'drag';
      if (!hover) cursor.classList.remove('on');
      wake();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        S.spinV += 26;
        wake();
      }
    };

    final.addEventListener('pointermove', onMove);
    mark.addEventListener('pointerenter', onEnter);
    mark.addEventListener('pointerleave', onLeave);
    mark.addEventListener('pointerdown', onDown);
    mark.addEventListener('pointerup', release);
    mark.addEventListener('pointercancel', release);
    mark.addEventListener('keydown', onKey);
    const io = new IntersectionObserver((es) => {
      visible = es[0].isIntersecting;
      wake();
    });
    io.observe(final);
    apply();

    return () => {
      cancelAnimationFrame(raf);
      io.disconnect();
      final.removeEventListener('pointermove', onMove);
      mark.removeEventListener('pointerenter', onEnter);
      mark.removeEventListener('pointerleave', onLeave);
      mark.removeEventListener('pointerdown', onDown);
      mark.removeEventListener('pointerup', release);
      mark.removeEventListener('pointercancel', release);
      mark.removeEventListener('keydown', onKey);
    };
  }, []);

  return (
    <>
      <div
        ref={markRef}
        className="loom-mark"
        role="img"
        // biome-ignore lint/a11y/noNoninteractiveTabindex: the mark spins from the keyboard too
        tabIndex={0}
        aria-label="The Loomplane mark. Drag it to pull the two threads apart; click to spin it."
      >
        <div className="lm-glow" aria-hidden="true" />
        <div ref={rigRef} className="lm-rig">
          <Thread
            id="lm-ivory"
            d={IVORY}
            face="var(--ink)"
            sheenId="lmSheenI"
            sheen="#ffffff"
            from="hsl(0 0% 30%)"
            to="hsl(0 0% 12%)"
          />
          <Thread
            id="lm-blue"
            d={BLUE}
            face="var(--blue)"
            sheenId="lmSheenB"
            sheen="#e6f3ff"
            from="oklch(0.5 0.1 240)"
            to="oklch(0.25 0.06 245)"
          />
        </div>
      </div>
      <div ref={cursorRef} className="lm-cursor" aria-hidden="true">
        <span ref={cursorText}>drag</span>
      </div>
    </>
  );
}
