"""A one-page brand sheet: lockups, construction, sizes and colors."""

import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import build  # noqa: E402
from mark import mark_paths, f, ink_points, to_d, scale_cmds, rotate_half  # noqa: E402
from wordmark import set_word  # noqa: E402

W, H = 1600, 1180
INK, PAPER, WELD, WELD_D = build.INK, build.PAPER, build.WELD, build.WELD_ON_DARK
MUTED = "#737373"
LINE = "#E5E5E5"


def label(text, x, y, size=15, fill=MUTED, weight="Medium"):
    d, _, _ = set_word(text, weight, size, 0)
    return f'<path fill="{fill}" transform="translate({f(x)} {f(y)})" d="{d}"/>'


def place(view_box, body, x, y, width):
    _, _, vw, vh = (float(v) for v in view_box.split())
    h = width * vh / vw
    return f'<svg x="{f(x)}" y="{f(y)}" width="{f(width)}" height="{f(h)}" viewBox="{view_box}">{body}</svg>', h


def main(out_dir):
    out = Path(out_dir)
    parts = [f'<rect width="{W}" height="{H}" fill="#FFFFFF"/>']

    # Hero: the horizontal logo.
    parts.append(f'<rect x="40" y="40" width="{W - 80}" height="380" rx="20" fill="#FFFFFF" stroke="{LINE}"/>')
    vb, body, _ = build.horizontal(INK, WELD, INK)
    el, h = place(vb, body, 0, 0, 760)
    parts.append(el.replace('x="0" y="0"', f'x="{f((W - 760) / 2)}" y="{f(230 - h / 2)}"'))
    parts.append(label("Primary logo", 72, 392))

    # Row two: dark, gold, stacked.
    y2, h2 = 450, 300
    cw = (W - 80 - 40) / 3
    xs = [40 + i * (cw + 20) for i in range(3)]
    parts.append(f'<rect x="{f(xs[0])}" y="{y2}" width="{f(cw)}" height="{h2}" rx="20" fill="#0A0A0A"/>')
    vb, body, _ = build.horizontal(PAPER, WELD_D, PAPER)
    el, h = place(vb, body, xs[0] + (cw - 380) / 2, y2 + h2 / 2 - 30, 380)
    parts.append(el)
    parts.append(label("On dark", xs[0] + 32, y2 + h2 - 28, fill="#A3A3A3"))

    parts.append(f'<rect x="{f(xs[1])}" y="{y2}" width="{f(cw)}" height="{h2}" rx="20" fill="{WELD}"/>')
    vb, body, _ = build.horizontal(INK, INK, INK)
    el, h = place(vb, body, xs[1] + (cw - 380) / 2, y2 + h2 / 2 - 30, 380)
    parts.append(el)
    parts.append(label("One color", xs[1] + 32, y2 + h2 - 28, fill=INK))

    parts.append(f'<rect x="{f(xs[2])}" y="{y2}" width="{f(cw)}" height="{h2}" rx="20" fill="#FFFFFF" stroke="{LINE}"/>')
    vb, body, _ = build.stacked(INK, WELD, INK)
    el, h = place(vb, body, xs[2] + (cw - 230) / 2, y2 + 46, 230)
    parts.append(el)
    parts.append(label("Stacked", xs[2] + 32, y2 + h2 - 28))

    # Row three: construction, sizes, app icon, colors.
    y3, h3 = 780, 360
    parts.append(f'<rect x="40" y="{y3}" width="{W - 80}" height="{h3}" rx="20" fill="#FAFAFA" stroke="{LINE}"/>')

    # Construction: ink L + gold L = the mark.
    spec = build.PRIMARY
    s = 2.7
    side = spec.size * s
    cy0 = y3 + 70
    ink = ink_points(spec)
    gold = rotate_half(ink, spec)
    x = 72
    parts.append(f'<path fill="{INK}" d="{to_d(scale_cmds(ink, s, x, cy0))}"/>')
    x += side + 22
    parts.append(label("+", x, cy0 + side / 2 + 10, size=30, fill=MUTED))
    x += 42
    parts.append(f'<path fill="{WELD}" d="{to_d(scale_cmds(gold, s, x, cy0))}"/>')
    x += side + 22
    parts.append(label("=", x, cy0 + side / 2 + 10, size=30, fill=MUTED))
    x += 42
    ink_d, gold_d = mark_paths(spec, s, x, cy0)
    parts.append(f'<path fill="{INK}" d="{ink_d}"/><path fill="{WELD}" d="{gold_d}"/>')
    parts.append(label("Two L threads, each tucked under the other", 72, y3 + h3 - 32))

    # Sizes.
    sx = 560
    x = sx
    for size in (96, 64, 40, 24, 16):
        spec_used = build.PRIMARY if size >= 24 else build.SMALL
        ink_d, gold_d = mark_paths(spec_used, size / 32, x, y3 + 70 + (96 - size))
        parts.append(f'<path fill="{INK}" d="{ink_d}"/><path fill="{WELD}" d="{gold_d}"/>')
        parts.append(label(f"{size}", x, y3 + 200, size=13))
        x += size + 28
    parts.append(label("Mark sizes", sx, y3 + h3 - 32))

    # App icon.
    ax = 960
    side = 150
    parts.append(f'<rect x="{ax}" y="{y3 + 60}" width="{side}" height="{side}" rx="{f(side * 0.225)}" fill="{INK}"/>')
    ink_d, gold_d = mark_paths(spec, side * 0.54 / 32, ax + side * 0.23, y3 + 60 + side * 0.23)
    parts.append(f'<path fill="{PAPER}" d="{ink_d}"/><path fill="{WELD_D}" d="{gold_d}"/>')
    parts.append(label("App icon", ax, y3 + h3 - 32))

    # Colors.
    px = 1160
    swatches = [
        ("Ink", INK, "#171717", PAPER),
        ("Weld", WELD, "#D8A00D", INK),
        ("Weld on dark", WELD_D, "#E3AE3B", INK),
        ("Paper", PAPER, "#FAFAFA", INK),
    ]
    for i, (name, color, hexv, fg) in enumerate(swatches):
        yy = y3 + 52 + i * 62
        stroke = f' stroke="{LINE}"' if color == PAPER else ""
        parts.append(f'<rect x="{px}" y="{yy}" width="360" height="52" rx="10" fill="{color}"{stroke}/>')
        parts.append(label(name, px + 18, yy + 32, size=15, fill=fg, weight="SemiBold"))
        parts.append(label(hexv, px + 250, yy + 32, size=15, fill=fg))
    parts.append(label("Colors", px, y3 + h3 - 32 + 0))

    svg = f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">{"".join(parts)}</svg>'
    (out / "preview.svg").write_text(svg)
    subprocess.run(["rsvg-convert", "-o", str(out / "preview.png"), str(out / "preview.svg")], check=True)
    (out / "preview.svg").unlink()
    print("preview", out / "preview.png")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else str(Path(__file__).parent.parent))
