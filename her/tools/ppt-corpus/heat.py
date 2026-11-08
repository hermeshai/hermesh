"""One slide's reference and candidate blended in grey, with the pixels that differ in red.

    heat.py REFERENCE.png CANDIDATE.png OUT.png
"""
import sys

from PIL import Image, ImageChops

THRESHOLD = 40


def main(reference, candidate, out):
    a = Image.open(reference).convert("RGB")
    b = Image.open(candidate).convert("RGB")
    if abs(a.width / a.height - b.width / b.height) > 0.005:
        raise ValueError(f"aspect mismatch: reference {a.size}, candidate {b.size}")
    if b.size != a.size:
        b = b.resize(a.size, Image.LANCZOS)
    mask = ImageChops.difference(a, b).convert("L").point(lambda v: 255 if v > THRESHOLD else 0)
    base = Image.blend(a, b, 0.5).convert("L").convert("RGB")
    Image.composite(Image.new("RGB", a.size, (255, 0, 0)), base, mask).save(out)


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    main(*sys.argv[1:])
