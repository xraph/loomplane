"""Build every Loomplane logo asset from one geometry source.

Usage: LOOMPLANE_FONTS=<dir with Geist TTFs> python3 brand/source/build.py [out_dir]
out_dir defaults to brand/. preview.py draws brand/preview.png the same way.
"""

import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from mark import MarkSpec, mark_paths, f  # noqa: E402
from wordmark import set_word, ink_bounds  # noqa: E402

from PIL import Image  # noqa: E402

INK = "#171717"
PAPER = "#FAFAFA"
WELD = "#D8A00D"
WELD_ON_DARK = "#E3AE3B"

PRIMARY = MarkSpec(size=32, w=8, radius=11, gap=2.5)
# Below about 24px the gaps close up, so small sizes get heavier threads and
# wider gaps.
SMALL = MarkSpec(size=32, w=9, radius=12, gap=3)

WORD = "loomplane"
WEIGHT = "SemiBold"
FONT_SIZE = 100.0
TRACKING = -10  # font units, a touch tighter than Geist's default

PALETTES = {
    "": (INK, WELD, INK),  # (ink thread, gold thread, wordmark)
    "-dark": (PAPER, WELD_ON_DARK, PAPER),
    "-black": (INK, INK, INK),
    "-white": (PAPER, PAPER, PAPER),
}


def svg(view_box, body, title="Loomplane"):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{view_box}" role="img" '
        f'aria-label="{title}"><title>{title}</title>{body}</svg>\n'
    )


def mark_body(spec, ink, gold, scale=1.0, ox=0.0, oy=0.0):
    ink_d, gold_d = mark_paths(spec, scale, ox, oy)
    return f'<path fill="{ink}" d="{ink_d}"/><path fill="{gold}" d="{gold_d}"/>'


def word():
    d, right, m = set_word(WORD, WEIGHT, FONT_SIZE, TRACKING)
    top, bottom = ink_bounds(WORD, WEIGHT, FONT_SIZE)
    return d, m["ink_left"], right, top, bottom


def horizontal(ink, gold, text):
    d, left, right, top, bottom = word()
    l_height = -top  # the l's ascender sets the word's optical height
    mark_h = l_height * 1.2
    gap = mark_h * 0.34
    centre = top / 2
    mark_top = centre - mark_h / 2
    s = mark_h / PRIMARY.size
    body = mark_body(PRIMARY, ink, gold, s, 0, mark_top)
    tx = mark_h + gap - left
    body += f'<path fill="{text}" transform="translate({f(tx)} 0)" d="{d}"/>'
    y0 = min(mark_top, top)
    y1 = max(mark_top + mark_h, bottom)
    w = tx + right
    return f"0 {f(y0)} {f(w)} {f(y1 - y0)}", body, (w, y1 - y0)


def stacked(ink, gold, text):
    d, left, right, top, bottom = word()
    word_w = right - left
    mark_h = word_w * 0.3
    gap = mark_h * 0.3
    s = mark_h / PRIMARY.size
    mx = (word_w - mark_h) / 2
    body = mark_body(PRIMARY, ink, gold, s, mx, 0)
    ty = mark_h + gap - top
    body += f'<path fill="{text}" transform="translate({f(-left)} {f(ty)})" d="{d}"/>'
    h = ty + bottom
    return f"0 0 {f(word_w)} {f(h)}", body, (word_w, h)


def wordmark(text):
    d, left, right, top, bottom = word()
    body = f'<path fill="{text}" transform="translate({f(-left)} 0)" d="{d}"/>'
    return f"0 {f(top)} {f(right - left)} {f(bottom - top)}", body


def favicon_svg():
    ink_d, gold_d = mark_paths(SMALL)
    style = (
        f"<style>.i{{fill:{INK}}}.g{{fill:{WELD}}}"
        f"@media (prefers-color-scheme:dark){{.i{{fill:{PAPER}}}.g{{fill:{WELD_ON_DARK}}}}}</style>"
    )
    body = f'{style}<path class="i" d="{ink_d}"/><path class="g" d="{gold_d}"/>'
    return svg("0 0 32 32", body)


def app_icon_svg(rounded=True):
    side = 512.0
    mark = side * 0.54
    s = mark / PRIMARY.size
    o = (side - mark) / 2
    rx = f' rx="{f(side * 0.225)}"' if rounded else ""
    body = f'<rect width="{f(side)}" height="{f(side)}"{rx} fill="{INK}"/>'
    body += mark_body(PRIMARY, PAPER, WELD_ON_DARK, s, o, o)
    return svg(f"0 0 {f(side)} {f(side)}", body)


def render(svg_path, png_path, width=None, height=None):
    cmd = ["rsvg-convert", "-o", str(png_path)]
    if width:
        cmd += ["-w", str(width)]
    if height:
        cmd += ["-h", str(height)]
    cmd.append(str(svg_path))
    subprocess.run(cmd, check=True)


def main(out):
    out = Path(out)
    logo, icon, png = out / "logo", out / "icon", out / "png"
    for p in (logo, icon, png):
        p.mkdir(parents=True, exist_ok=True)

    for suffix, (ink, gold, text) in PALETTES.items():
        vb, body = f"0 0 {f(PRIMARY.size)} {f(PRIMARY.size)}", mark_body(PRIMARY, ink, gold)
        (logo / f"loomplane-mark{suffix}.svg").write_text(svg(vb, body))
        vb, body, _ = horizontal(ink, gold, text)
        (logo / f"loomplane-logo{suffix}.svg").write_text(svg(vb, body))
        vb, body, _ = stacked(ink, gold, text)
        (logo / f"loomplane-logo-stacked{suffix}.svg").write_text(svg(vb, body))
        if suffix in ("", "-dark"):  # the wordmark has no gold, so two cover it
            vb, body = wordmark(text)
            (logo / f"loomplane-wordmark{suffix}.svg").write_text(svg(vb, body))

    (icon / "favicon.svg").write_text(favicon_svg())
    (icon / "app-icon.svg").write_text(app_icon_svg(rounded=True))
    (icon / "app-icon-square.svg").write_text(app_icon_svg(rounded=False))

    # Raster exports.
    for n in (16, 32, 48):
        render(icon / "favicon.svg", icon / f"favicon-{n}.png", n, n)
    imgs = [Image.open(icon / f"favicon-{n}.png").convert("RGBA") for n in (16, 32, 48)]
    imgs[-1].save(icon / "favicon.ico", sizes=[(16, 16), (32, 32), (48, 48)], append_images=imgs[:-1])
    render(icon / "app-icon-square.svg", icon / "apple-touch-icon.png", 180, 180)
    for n in (192, 512):
        render(icon / "app-icon.svg", icon / f"icon-{n}.png", n, n)
    for suffix in ("", "-dark"):
        render(logo / f"loomplane-logo{suffix}.svg", png / f"loomplane-logo{suffix}.png", 1200)
        render(logo / f"loomplane-logo-stacked{suffix}.svg", png / f"loomplane-logo-stacked{suffix}.png", 800)
        render(logo / f"loomplane-mark{suffix}.svg", png / f"loomplane-mark{suffix}.png", 512, 512)
    print("built", out)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else str(Path(__file__).parent.parent))
