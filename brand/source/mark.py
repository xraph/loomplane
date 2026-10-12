"""The Loomplane mark as plain filled paths.

Two L-shaped threads make a square. The ink thread is an L (left side and
bottom); the gold thread is the same L turned half a revolution (top and right).
Each one tucks under the other at one corner, so the square reads as woven from
two threads.
"""

from dataclasses import dataclass


def f(v):
    s = f"{v:.3f}".rstrip("0").rstrip(".")
    return "0" if s in ("-0", "") else s


@dataclass
class MarkSpec:
    size: float = 32.0  # side of the square
    w: float = 7.5  # thread width
    radius: float = 10.0  # outer radius of the L's corner
    gap: float = 2.25  # gap where a thread passes under the other

    @property
    def inner_radius(self):
        return max(self.radius - self.w, 0.0)


def ink_points(m: MarkSpec, ox=0.0, oy=0.0):
    """Path commands for the ink L, as (cmd, args) with absolute coordinates."""
    lo, hi, w, R, r, g = 0.0, m.size, m.w, m.radius, m.inner_radius, m.gap
    c = w / 2
    cmds = [
        ("M", (lo, lo + w + g)),
        ("L", (lo, hi - R)),
        ("A", (R, R, 0, 0, 0, lo + R, hi)),
        ("L", (hi - c, hi)),
        ("A", (c, c, 0, 0, 0, hi - c, hi - w)),
        ("L", (lo + w + r, hi - w)),
    ]
    if r > 0:
        cmds.append(("A", (r, r, 0, 0, 1, lo + w, hi - w - r)))
    cmds.append(("L", (lo + w, lo + w + g)))
    cmds.append(("Z", ()))
    return [(k, shift(k, a, ox, oy)) for k, a in cmds]


def rotate_half(cmds, m: MarkSpec, ox=0.0, oy=0.0):
    """Turn a path half a revolution about the square's centre."""
    cx, cy = ox + m.size / 2, oy + m.size / 2
    out = []
    for k, a in cmds:
        if k in ("M", "L"):
            out.append((k, (2 * cx - a[0], 2 * cy - a[1])))
        elif k == "A":
            rx, ry, rot, large, sweep, x, y = a
            out.append((k, (rx, ry, rot, large, sweep, 2 * cx - x, 2 * cy - y)))
        else:
            out.append((k, a))
    return out


def shift(k, a, ox, oy):
    if k in ("M", "L"):
        return (a[0] + ox, a[1] + oy)
    if k == "A":
        rx, ry, rot, large, sweep, x, y = a
        return (rx, ry, rot, large, sweep, x + ox, y + oy)
    return a


def scale_cmds(cmds, s, ox=0.0, oy=0.0):
    out = []
    for k, a in cmds:
        if k in ("M", "L"):
            out.append((k, (a[0] * s + ox, a[1] * s + oy)))
        elif k == "A":
            rx, ry, rot, large, sweep, x, y = a
            out.append((k, (rx * s, ry * s, rot, large, sweep, x * s + ox, y * s + oy)))
        else:
            out.append((k, a))
    return out


def to_d(cmds):
    parts = []
    for k, a in cmds:
        if k == "Z":
            parts.append("Z")
        elif k == "A":
            rx, ry, rot, large, sweep, x, y = a
            parts.append(f"A{f(rx)} {f(ry)} {f(rot)} {large} {sweep} {f(x)} {f(y)}")
        else:
            parts.append(f"{k}{f(a[0])} {f(a[1])}")
    return "".join(parts)


def mark_paths(m: MarkSpec, scale=1.0, ox=0.0, oy=0.0):
    """Returns (ink_d, gold_d) for the mark scaled and placed at (ox, oy)."""
    ink = ink_points(m)
    gold = rotate_half(ink, m)
    return to_d(scale_cmds(ink, scale, ox, oy)), to_d(scale_cmds(gold, scale, ox, oy))
