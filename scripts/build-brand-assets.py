#!/usr/bin/env python3
"""Generate UI images from the supplied SVG masters. See assets/branding/README.md."""

from io import BytesIO
from pathlib import Path

import cairosvg
from PIL import Image, ImageOps


ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "assets" / "branding"
OUTPUT = ROOT / "web" / "public" / "brand"
OUTPUT.mkdir(parents=True, exist_ok=True)


def render(name):
    # Render at twice the native size before downsampling for clean small edges.
    return Image.open(BytesIO(cairosvg.svg2png(
        url=str(SOURCE / name), output_width=2508, output_height=2508,
    ))).convert("RGBA")


def square(image, size, fraction=0.94, background=(0, 0, 0, 0)):
    result = Image.new("RGBA", (size, size), background)
    fitted = ImageOps.contain(image, (round(size * fraction),) * 2, Image.Resampling.LANCZOS)
    result.alpha_composite(fitted, ((size - fitted.width) // 2, (size - fitted.height) // 2))
    return result


core = render("Doodship-Core-Logo.svg")
logo = core.crop(core.getbbox())
# Crop the symbol above the wordmark. Coordinates use the original 1254px canvas.
symbol = core.crop((208 * 2, 28 * 2, 1048 * 2, 980 * 2))
courier = render("Doodploy-Courier-Mascot.svg")
courier = courier.crop(courier.getbbox())

for name, image in (("doodship-logo", logo), ("doodship-courier", courier)):
    for size in (256, 512):
        square(image, size).save(OUTPUT / f"{name}-{size}.webp", quality=90, method=6)

for size in (32, 64, 128):
    square(symbol, size).save(OUTPUT / f"doodship-icon-{size}.png", optimize=True)

square(symbol, 64).save(OUTPUT / "favicon.ico", sizes=[(16, 16), (32, 32), (48, 48), (64, 64)])
square(symbol, 180, fraction=0.78, background=(246, 248, 251, 255)).save(
    OUTPUT / "apple-touch-icon.png", optimize=True,
)
for size in (192, 512):
    # A centred square inside the maskable safe circle has a side below 56.5%.
    square(symbol, size, fraction=0.55, background=(246, 248, 251, 255)).save(
        OUTPUT / f"doodship-app-{size}.png", optimize=True,
    )

for path in sorted(OUTPUT.iterdir()):
    print(f"{path.relative_to(ROOT)}: {path.stat().st_size:,} bytes")
