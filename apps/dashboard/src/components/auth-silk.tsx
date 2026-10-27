import { useEffect, useRef } from "react"

import { silkFragment } from "./auth-silk-shader"

const VERTEX = "attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}"

/** The landing site's hero light, drawn with WebGL. Without it, a still gradient shows. */
export function AuthSilk() {
  const canvas = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const cv = canvas.current
    if (!cv) return
    const gl = cv.getContext("webgl", {
      antialias: false,
      premultipliedAlpha: false,
      alpha: false,
    })
    if (!gl) return
    const compile = (type: number, src: string) => {
      const sh = gl.createShader(type)
      if (!sh) return null
      gl.shaderSource(sh, src)
      gl.compileShader(sh)
      return gl.getShaderParameter(sh, gl.COMPILE_STATUS) ? sh : null
    }
    const vs = compile(gl.VERTEX_SHADER, VERTEX)
    const fs = compile(gl.FRAGMENT_SHADER, silkFragment)
    const prog = gl.createProgram()
    if (!vs || !fs || !prog) return
    gl.attachShader(prog, vs)
    gl.attachShader(prog, fs)
    gl.linkProgram(prog)
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return
    gl.useProgram(prog)
    gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer())
    gl.bufferData(
      gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 3, -1, -1, 3]),
      gl.STATIC_DRAW
    )
    const loc = gl.getAttribLocation(prog, "p")
    gl.enableVertexAttribArray(loc)
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0)
    const uRes = gl.getUniformLocation(prog, "res")
    const uT = gl.getUniformLocation(prog, "t")
    const uM = gl.getUniformLocation(prog, "m")
    const uCs = gl.getUniformLocation(prog, "cs")
    cv.dataset.ready = "true"

    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches
    const start = performance.now()
    let cell = 4
    let raf = 0

    const frame = (ms: number) => {
      gl.uniform2f(uRes, cv.width, cv.height)
      gl.uniform1f(uT, ms / 1000)
      gl.uniform2f(uM, 0, 0)
      gl.uniform1f(uCs, cell)
      gl.drawArrays(gl.TRIANGLES, 0, 3)
    }
    const resize = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      cell = Math.max(2, Math.round(3 * dpr))
      cv.width = Math.max(1, Math.floor(cv.clientWidth * dpr))
      cv.height = Math.max(1, Math.floor(cv.clientHeight * dpr))
      gl.viewport(0, 0, cv.width, cv.height)
      if (reduce) frame(18000)
    }
    const loop = (now: number) => {
      if (!document.hidden) frame(now - start + 9000)
      raf = requestAnimationFrame(loop)
    }

    resize()
    const ro = new ResizeObserver(resize)
    ro.observe(cv)
    if (reduce) frame(18000)
    else raf = requestAnimationFrame(loop)

    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
    }
  }, [])

  return <canvas ref={canvas} className="auth-silk" />
}
