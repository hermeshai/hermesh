# PPT comparison tools

Compares the slides that her-slide (BetterOffice) draws with reference images exported from PowerPoint, slide by slide, and checks that a save changed only the edited parts. The scripts need Python 3 and Pillow.

Samples and reference images are not in the repository; they go in gitignored directories:

```
tools/ppt-corpus/corpus/<deck>.pptx        test deck
tools/ppt-corpus/corpus/pp/<deck>-<n>.png  reference image from PowerPoint (in PowerPoint, "Export → PNG"; n starts at 1)
tools/ppt-corpus/out/                      render results and comparison images
```

## Render

```bash
cargo run --release -p her-slide --example render -- tools/ppt-corpus/corpus/deck_office.pptx tools/ppt-corpus/out/bo 1080
```

## Slide-by-slide comparison

The first `NAME=DIR` is the reference; for each other column it prints the mean RGB difference from the reference and the share of pixels whose grey difference is over 40, and it makes a side-by-side sheet:

Scores are computed at the reference image's original size; a candidate image of a different size is scaled proportionally. When the aspect ratios differ, it is marked `ASPECT MISMATCH` and gets no score; a missing image is marked `MISSING`. So scores from the new version cannot always be compared directly with the old version's, which always stretched images to 1080×607.

```bash
python3 tools/ppt-corpus/compare.py deck_office tools/ppt-corpus/out/compare_deck_office.png PowerPoint=tools/ppt-corpus/corpus/pp BetterOffice=tools/ppt-corpus/out/bo
```

A heat map for one slide, with differing pixels in red:

```bash
python3 tools/ppt-corpus/heat.py tools/ppt-corpus/corpus/pp/deck_office-2.png tools/ppt-corpus/out/bo/deck_office-2.png tools/ppt-corpus/out/heat-2.png
```

The heat map also needs both images to have the same aspect ratio.

## Save check

Lists the parts whose bytes changed after a save; the exit code is 1 if any changed:

```bash
python3 tools/ppt-corpus/zipdiff.py original.pptx saved.pptx
```

`Deck::save` runs the same check itself: if any part other than the edited slides changed, it returns an error.

## Integration tests

her-slide's integration tests read the same samples:

```bash
HER_PPT_SAMPLES=tools/ppt-corpus/corpus cargo test -p her-slide
```

Without `HER_PPT_SAMPLES` set, these tests pass without running. Setting `HER_PPT_RENDER_OUT=<dir>` writes the render results to that directory.
