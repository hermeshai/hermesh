//! Renders every slide of a deck: `render DECK OUT_DIR [WIDTH_PX]`, writing `OUT_DIR/<stem>-<n>.png`.
//! tools/ppt-corpus compares these with PowerPoint's.

use std::path::Path;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let [deck, out, rest @ ..] = args.as_slice() else {
        return Err("usage: render DECK OUT_DIR [WIDTH_PX]".into());
    };
    let width = rest.first().map_or(Ok(1080), |w| w.parse())?;
    let stem = Path::new(deck)
        .file_stem()
        .ok_or("deck has no name")?
        .to_string_lossy();
    let deck = her_slide::Deck::open(std::fs::read(deck)?)?;
    std::fs::create_dir_all(out)?;
    for slide in 0..deck.slide_count() {
        let png = deck.render_png(slide, width)?;
        std::fs::write(
            Path::new(out).join(format!("{stem}-{}.png", slide + 1)),
            png,
        )?;
    }
    Ok(())
}
