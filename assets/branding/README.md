# Doodship artwork

The supplied SVGs are the original masters. Keep them unchanged. They are not
served by the UI because each file is several megabytes.

The UI uses images in `web/public/brand`:

- Logo and courier mascot: transparent 256px and 512px WebP images.
- Navigation icon: 32px, 64px and 128px PNG images, cropped above the wordmark.
- Favicon: an ICO file containing 16px, 32px, 48px and 64px images.
- Apple touch icon: 180px PNG with a light background.
- App shortcuts: 192px and 512px PNG images with padding for maskable icons.

To regenerate them, install Cairo on your system, then run:

```sh
python3 -m venv /tmp/doodship-brand-tools
/tmp/doodship-brand-tools/bin/pip install -r scripts/branding-requirements.txt
/tmp/doodship-brand-tools/bin/python scripts/build-brand-assets.py
```

The full logo includes a dark wordmark. Display it on a light background. The
small navigation icon works on both light and dark backgrounds.
