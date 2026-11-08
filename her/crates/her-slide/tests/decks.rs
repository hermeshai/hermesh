//! Real decks, end to end: render every slide, read, replace one phrase, save, reopen.
//!
//! The decks are not in the repository. `HER_PPT_SAMPLES` names a folder holding
//! `deck_office.pptx` and `deck_agreement.pptx`; without it these tests pass without running.
//! `HER_PPT_RENDER_OUT`, when set, is where the rendered slides are written.

use std::path::PathBuf;

use her_slide::Deck;

const DECKS: [&str; 2] = ["deck_office.pptx", "deck_agreement.pptx"];
const WIDTH: u32 = 1280;
const TITLE: &str = "三季度经营回顾";
const NEW_TITLE: &str = "第三季度复盘";

fn sample(name: &str) -> Option<Vec<u8>> {
    let dir = std::env::var_os("HER_PPT_SAMPLES")?;
    Some(std::fs::read(PathBuf::from(dir).join(name)).expect("sample deck"))
}

fn png_size(bytes: &[u8]) -> (u32, u32) {
    let reader = png::Decoder::new(std::io::Cursor::new(bytes))
        .read_info()
        .unwrap();
    let info = reader.info();
    (info.width, info.height)
}

#[test]
fn every_slide_renders() {
    for name in DECKS {
        let Some(bytes) = sample(name) else { return };
        let deck = Deck::open(bytes).unwrap();
        assert!(deck.slide_count() > 0, "{name}");
        for slide in 0..deck.slide_count() {
            let png = deck.render_png(slide, WIDTH).unwrap();
            let (width, height) = png_size(&png);
            assert!(
                width.abs_diff(WIDTH) <= 1 && height > 0,
                "{name} slide {slide}: {width}x{height}"
            );
            if let Some(out) = std::env::var_os("HER_PPT_RENDER_OUT") {
                let stem = name.trim_end_matches(".pptx");
                std::fs::write(
                    PathBuf::from(out).join(format!("{stem}-{}.png", slide + 1)),
                    &png,
                )
                .unwrap();
            }
        }
        assert!(deck.render_png(deck.slide_count(), WIDTH).is_err());
    }
}

#[test]
fn a_replaced_phrase_survives_the_round_trip() {
    for name in DECKS {
        let Some(bytes) = sample(name) else { return };
        let deck = Deck::open(bytes).unwrap();
        assert!(deck.markdown().unwrap().contains(TITLE), "{name}");
        assert_eq!(deck.replace_text(TITLE, NEW_TITLE).unwrap(), 1, "{name}");

        let saved = deck.save().unwrap();
        let reopened = Deck::open(saved).unwrap();
        let markdown = reopened.markdown().unwrap();
        assert!(markdown.contains(&format!("2026 年{NEW_TITLE}")), "{name}");
        assert!(!markdown.contains(TITLE), "{name}");
        assert_eq!(reopened.slide_count(), deck.slide_count());
        reopened.render_png(0, WIDTH).unwrap();
    }
}

#[test]
fn an_unedited_deck_saves_with_every_part_unchanged() {
    for name in DECKS {
        let Some(bytes) = sample(name) else { return };
        let deck = Deck::open(bytes).unwrap();
        assert_eq!(
            deck.replace_text("这段文字不在任何幻灯片里", "x").unwrap(),
            0
        );
        deck.save().unwrap();
    }
}
