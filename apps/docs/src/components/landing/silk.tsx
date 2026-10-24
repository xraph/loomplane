"use client";

import { usePathname } from "next/navigation";
import { useEffect, useRef } from "react";
import { silkFragment } from "./silk-shader";

const VERTEX = "attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}";

/** The animated light behind each page's hero. Falls back to a still gradient without WebGL. */
export function Silk() {
  const pathname = usePathname();
  const stage = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const fallback = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const cv = canvas.current;
    const st = stage.current;
    if (!cv || !st) return;
    const gl = cv.getContext("webgl", {
      antialias: false,
      premultipliedAlpha: false,
      alpha: false,
    });
    if (!gl) {
      cv.hidden = true;
      return;
    }
    const compile = (type: number, src: string) => {
      const sh = gl.createShader(type);
      if (!sh) return null;
      gl.shaderSource(sh, src);
      gl.compileShader(sh);
      return gl.getShaderParameter(sh, gl.COMPILE_STATUS) ? sh : null;
    };
    const vs = compile(gl.VERTEX_SHADER, VERTEX);
    const fs = compile(gl.FRAGMENT_SHADER, silkFragment);
    const prog = gl.createProgram();
    if (!vs || !fs || !prog) {
      cv.hidden = true;
      return;
    }
    gl.attachShader(prog, vs);
    gl.attachShader(prog, fs);
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
      cv.hidden = true;
      return;
    }
    // biome-ignore lint/correctness/useHookAtTopLevel: WebGL's useProgram, not a React hook
    gl.useProgram(prog);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(
      gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 3, -1, -1, 3]),
      gl.STATIC_DRAW,
    );
    const loc = gl.getAttribLocation(prog, "p");
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
    const uRes = gl.getUniformLocation(prog, "res");
    const uT = gl.getUniformLocation(prog, "t");
    const uM = gl.getUniformLocation(prog, "m");
    const uCs = gl.getUniformLocation(prog, "cs");
    if (fallback.current) fallback.current.style.display = "none";

    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const mouse = [0, 0];
    const target = [0, 0];
    const start = performance.now();
    let cellPx = 4;
    let raf = 0;

    const frame = (ms: number) => {
      mouse[0] += (target[0] - mouse[0]) * 0.04;
      mouse[1] += (target[1] - mouse[1]) * 0.04;
      gl.uniform2f(uRes, cv.width, cv.height);
      gl.uniform1f(uT, ms / 1000);
      gl.uniform2f(uM, mouse[0], mouse[1]);
      gl.uniform1f(uCs, cellPx);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
    };
    const resize = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      cellPx = Math.max(2, Math.round(3 * dpr));
      cv.width = Math.max(1, Math.floor(cv.clientWidth * dpr));
      cv.height = Math.max(1, Math.floor(cv.clientHeight * dpr));
      gl.viewport(0, 0, cv.width, cv.height);
      if (reduce) frame(18000);
    };
    const loop = (now: number) => {
      if (!document.hidden && window.scrollY < st.offsetHeight)
        frame(now - start + 9000);
      raf = requestAnimationFrame(loop);
    };
    const onMove = (e: PointerEvent) => {
      target[0] = (e.clientX / window.innerWidth - 0.5) * 2;
      target[1] = -(e.clientY / window.innerHeight - 0.5) * 2;
    };

    resize();
    const ro = new ResizeObserver(resize);
    ro.observe(cv);
    window.addEventListener("pointermove", onMove, { passive: true });
    if (reduce) frame(18000);
    else raf = requestAnimationFrame(loop);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      window.removeEventListener("pointermove", onMove);
      // the context stays alive so a remount (and React's dev double effects) can reuse it
    };
  }, []);

  return (
    <div
      ref={stage}
      className={pathname === "/" ? "stage" : "stage sub"}
      aria-hidden="true"
    >
      <div ref={fallback} className="stage-fallback" />
      <canvas ref={canvas} id="silk" />
    </div>
  );
}
