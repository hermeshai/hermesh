"""Side-by-side sheet of a deck's slides as each renderer draws them, with pixel differences
from the first one (the reference, normally PowerPoint).

    compare.py DECK OUT.png NAME=DIR [NAME=DIR ...]

Each DIR holds DECK-<n>.png for n = 1, 2, ...; the first NAME=DIR is the reference. Prints one
line per slide: mean absolute RGB difference, and the share of pixels whose grey difference
exceeds 40.
"""
import os
import sys
import glob
import re

from PIL import Image, ImageChops, ImageDraw, ImageFont

SIZE = (1080, 607)
SCALE = 0.5
THRESHOLD = 40


def load(path):
    if not os.path.exists(path):
        return None
    image = Image.open(path).convert("RGB")
    return image


def difference(reference, image):
    diff = ImageChops.difference(reference, image)
    mean = sum(sum(p) for p in diff.getdata()) / (3 * reference.width * reference.height)
    grey = diff.convert("L").getdata()
    over = sum(1 for v in grey if v > THRESHOLD) / len(grey)
    return mean, over


def main(deck, out, columns):
    columns = [c.split("=", 1) for c in columns]
    numbers = set()
    for _, directory in columns:
        for path in glob.glob(os.path.join(directory, f"{glob.escape(deck)}-*.png")):
            match = re.fullmatch(re.escape(deck) + r'-(\d+)\.png', os.path.basename(path))
            if match:
                numbers.add(int(match.group(1)))
    if not numbers:
        raise ValueError(f"no images found for {deck}")
    slides = max(numbers)
    try:
        font = ImageFont.truetype("/System/Library/Fonts/Helvetica.ttc", 22)
    except OSError:
        font = ImageFont.load_default()
    cw, ch = int(SIZE[0] * SCALE), int(SIZE[1] * SCALE)
    sheet = Image.new("RGB", (cw * len(columns) + 10 * (len(columns) + 1), (ch + 40) * slides + 40), "white")
    draw = ImageDraw.Draw(sheet)
    print(f"deck={deck} reference={columns[0][0]}")
    for n in range(1, slides + 1):
        images = [load(f"{d}/{deck}-{n}.png") for _, d in columns]
        row = []
        for i, (name, _) in enumerate(columns):
            x, y = 10 + i * (cw + 10), 40 + (n - 1) * (ch + 40)
            label = name
            if images[i] is not None:
                preview = images[i].copy()
                preview.thumbnail((cw, ch), Image.LANCZOS)
                sheet.paste(preview, (x + (cw - preview.width) // 2, y + (ch - preview.height) // 2))
                if i > 0 and images[0] is not None:
                    ref, candidate = images[0], images[i]
                    # Allow normal one-pixel rounding on independently sized exports.
                    if abs(ref.width / ref.height - candidate.width / candidate.height) > 0.005:
                        label += "  ASPECT MISMATCH"
                        row.append(f"{name}: aspect mismatch {ref.size} vs {candidate.size}")
                    else:
                        if candidate.size != ref.size:
                            candidate = candidate.resize(ref.size, Image.LANCZOS)
                        mean, over = difference(ref, candidate)
                        label += f"  mean={mean:.1f}  >{THRESHOLD}={over * 100:.1f}%"
                        row.append(f"{name}: mean={mean:.2f} >{THRESHOLD}={over * 100:.2f}%")
            else:
                label += "  MISSING"
                row.append(f"{name}: MISSING")
            draw.rectangle([x - 1, y - 1, x + cw, y + ch], outline="#999")
            draw.text((x, y - 30), f"p{n} {label}", fill="black", font=font)
        print(f"p{n}: " + " | ".join(row))
    sheet.save(out)


if __name__ == "__main__":
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2], sys.argv[3:])
