"""Set a word in Geist as outlined SVG paths, with the font's own GPOS pair
kerning, so the logo doesn't depend on the font being installed."""

import os
from pathlib import Path

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

# Geist's static TTFs (github.com/vercel/geist-font). Point LOOMPLANE_FONTS at
# the folder holding Geist-SemiBold.ttf and Geist-Medium.ttf.
FONTS = os.environ.get("LOOMPLANE_FONTS", str(Path(__file__).parent / "fonts"))


def pair_kerning(font, left, right):
    """X advance adjustment for a glyph pair from the font's GPOS kern feature."""
    gpos = font["GPOS"].table
    for rec in gpos.FeatureList.FeatureRecord:
        if rec.FeatureTag != "kern":
            continue
        for idx in rec.Feature.LookupListIndex:
            lookup = gpos.LookupList.Lookup[idx]
            for st in lookup.SubTable:
                if lookup.LookupType == 9:  # extension lookups wrap the real subtable
                    st = st.ExtSubTable
                if getattr(st, "LookupType", lookup.LookupType) != 2:
                    continue
                cov = st.Coverage.glyphs
                if left not in cov:
                    continue
                value = None
                if st.Format == 1:
                    for pv in st.PairSet[cov.index(left)].PairValueRecord:
                        if pv.SecondGlyph == right:
                            value = pv.Value1
                            break
                else:
                    c1 = st.ClassDef1.classDefs.get(left, 0)
                    c2 = st.ClassDef2.classDefs.get(right, 0)
                    value = st.Class1Record[c1].Class2Record[c2].Value1
                adv = getattr(value, "XAdvance", 0) if value is not None else 0
                if adv:
                    return adv
    return 0


def set_word(text, weight="SemiBold", size=100.0, tracking=0.0, adjust=None):
    """Returns (path_d, width, metrics). tracking and adjust are in font units;
    adjust maps a pair like "lo" to extra spacing."""
    font = TTFont(f"{FONTS}/Geist-{weight}.ttf")
    upm = font["head"].unitsPerEm
    scale = size / upm
    cmap = font.getBestCmap()
    gs = font.getGlyphSet()
    hmtx = font["hmtx"]
    adjust = adjust or {}
    x = 0.0
    parts = []
    names = [cmap[ord(ch)] for ch in text]
    ink_left = None
    for i, (ch, g) in enumerate(zip(text, names)):
        pen = SVGPathPen(gs, ntos=lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
        tpen = TransformPen(pen, (scale, 0, 0, -scale, x * scale, 0))
        gs[g].draw(tpen)
        parts.append(pen.getCommands())
        adv = hmtx[g][0]
        if ink_left is None:
            ink_left = hmtx[g][1]
        if i + 1 < len(text):
            adv += pair_kerning(font, g, names[i + 1]) + tracking
            adv += adjust.get(ch + text[i + 1], 0)
        x += adv
    os2 = font["OS/2"]
    # Right ink edge of the final glyph.
    last = names[-1]
    glyf = font["glyf"][last]
    width_ink = (x + glyf.xMax) * scale
    metrics = {
        "x_height": os2.sxHeight * scale,
        "cap_height": os2.sCapHeight * scale,
        "ascender": font["hhea"].ascent * scale,
        "descender": -font["hhea"].descent * scale,
        "ink_left": ink_left * scale,
        "ink_right": width_ink,
    }
    return " ".join(parts), width_ink, metrics


def ink_bounds(text, weight="SemiBold", size=100.0, tracking=0.0, adjust=None):
    """Vertical ink extent (top, bottom) in SVG coordinates, baseline at 0."""
    font = TTFont(f"{FONTS}/Geist-{weight}.ttf")
    scale = size / font["head"].unitsPerEm
    cmap = font.getBestCmap()
    glyf = font["glyf"]
    tops, bottoms = [], []
    for ch in text:
        g = glyf[cmap[ord(ch)]]
        if g.numberOfContours:
            tops.append(-g.yMax * scale)
            bottoms.append(-g.yMin * scale)
    return min(tops), max(bottoms)
