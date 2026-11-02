import { ImageResponse } from "next/og";

// The social preview for the landing pages: the mark, the line, and a band of the hero's light.
export const alt = "Loomplane: your infrastructure, woven together.";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

const IVORY =
  "M0 10.5L0 21A11 11 0 0 0 11 32L28 32A4 4 0 0 0 28 24L11 24A3 3 0 0 1 8 21L8 10.5Z";
const BLUE =
  "M32 21.5L32 11A11 11 0 0 0 21 0L4 0A4 4 0 0 0 4 8L21 8A3 3 0 0 1 24 11L24 21.5Z";

export default function Image() {
  return new ImageResponse(
    <div
      style={{
        width: "100%",
        height: "100%",
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        padding: "72px 80px",
        background: "#050505",
        backgroundImage:
          "linear-gradient(125deg, transparent 52%, rgba(47,92,120,0.55) 60%, rgba(90,174,229,0.35) 64%, transparent 70%, rgba(47,92,120,0.4) 78%, transparent 86%)",
        color: "#f5f5f5",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 20 }}>
        <svg width="56" height="56" viewBox="0 0 32 32" aria-hidden="true">
          <path fill="#f5f5f5" d={IVORY} />
          <path fill="#5aaee5" d={BLUE} />
        </svg>
        <span style={{ fontSize: 40, letterSpacing: -1 }}>loomplane</span>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 24 }}>
        <div
          style={{
            fontSize: 84,
            lineHeight: 1,
            letterSpacing: -4,
            display: "flex",
            flexDirection: "column",
          }}
        >
          <span>Your infrastructure,</span>
          <span style={{ color: "#d3dfee" }}>woven together.</span>
        </div>
        <div style={{ fontSize: 30, color: "#a3a3a3", maxWidth: 900 }}>
          A self-hosted deployment platform for the monorepos you already have.
        </div>
      </div>
    </div>,
    size,
  );
}
