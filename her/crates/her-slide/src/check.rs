//! Conservative checks based on the current engine snapshot. This module deliberately
//! does not claim text overflow, rotated bounds, or group-child geometry without layout.

use betteroffice_pptx::{DeckSnapshot, ShapeKind, ShapeSnapshot};
use her_protocol::deck::{DeckDiagnostic, DiagnosticRule};

/// Evaluate the implemented rules in slide/shape order; `index` is one-based.
/// Off-slide geometry is limited to unrotated top-level objects. Group descendants
/// use local transforms, so only their alternative text is checked.
pub fn check(deck: &DeckSnapshot) -> Vec<DeckDiagnostic> {
    let mut result = Vec::new();
    for (index, slide) in deck.slides.iter().enumerate() {
        if slide.hidden == Some(true) {
            continue;
        }
        for shape in &slide.shapes {
            if shape.hidden {
                continue;
            }
            if deck.width_emu > 0
                && deck.height_emu > 0
                && shape
                    .inherited
                    .as_ref()
                    .map_or(shape.rotation_deg, |r| r.rotation_deg)
                    == 0.0
            {
                let (x, y, width, height) = shape
                    .inherited
                    .as_ref()
                    .map_or((shape.x, shape.y, shape.width, shape.height), |r| {
                        (r.x, r.y, r.width, r.height)
                    });
                if outside(x, y, width, height, deck.width_emu, deck.height_emu) {
                    result.push(diagnostic(
                        DiagnosticRule::OffSlide,
                        &slide.id,
                        index,
                        &shape.id,
                        "对象超出幻灯片边界",
                    ));
                }
            }
            check_alt(shape, &slide.id, index, &mut result);
        }
    }
    result
}

fn outside(x: i64, y: i64, width: i64, height: i64, slide_width: i64, slide_height: i64) -> bool {
    width > 0
        && height > 0
        && (x < 0
            || y < 0
            || i128::from(x) + i128::from(width) > i128::from(slide_width)
            || i128::from(y) + i128::from(height) > i128::from(slide_height))
}

fn check_alt(
    shape: &ShapeSnapshot,
    slide_id: &str,
    index: usize,
    result: &mut Vec<DeckDiagnostic>,
) {
    if shape.hidden {
        return;
    }
    if matches!(shape.kind, ShapeKind::Picture)
        && !shape.decorative
        && is_blank(shape.title.as_deref())
        && is_blank(shape.description.as_deref())
    {
        result.push(diagnostic(
            DiagnosticRule::AltText,
            slide_id,
            index,
            &shape.id,
            "图片缺少替代文字",
        ));
    }
    for child in &shape.children {
        check_alt(child, slide_id, index, result);
    }
}

fn is_blank(text: Option<&str>) -> bool {
    text.is_none_or(|s| s.trim().is_empty())
}

fn diagnostic(
    rule: DiagnosticRule,
    slide_id: &str,
    index: usize,
    shape_id: &str,
    message: &str,
) -> DeckDiagnostic {
    let spec = rule.spec();
    DeckDiagnostic {
        id: format!(
            "{}:{}:{}:{}:{}",
            spec.id,
            slide_id.len(),
            slide_id,
            shape_id.len(),
            shape_id
        ),
        rule: spec.id.to_owned(),
        category: spec.category,
        severity: spec.severity,
        checker: spec.checker,
        slide_id: slide_id.to_owned(),
        index: (index + 1) as u32,
        shape_ids: vec![shape_id.to_owned()],
        text: None,
        message: message.to_owned(),
        fix: None,
        fix_label: None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bounds_are_inclusive_and_guard_overflow() {
        assert!(!outside(0, 0, 100, 50, 100, 50));
        assert!(outside(-1, 0, 100, 50, 100, 50));
        assert!(outside(0, 1, 100, 50, 100, 50));
        assert!(outside(i64::MAX, 0, 10, 1, 100, 50));
        assert!(!outside(0, 0, 0, 10, 100, 50));
    }

    #[test]
    fn blank_alternative_text_requires_non_whitespace() {
        assert!(is_blank(None));
        assert!(is_blank(Some(" \n")));
        assert!(!is_blank(Some("Revenue chart")));
    }

    #[test]
    fn diagnostics_use_the_shared_rule_metadata_and_one_based_pages() {
        let d = diagnostic(DiagnosticRule::OffSlide, "slide", 2, "shape", "message");
        assert_eq!(d.rule, "layout.off_slide");
        assert_eq!(d.index, 3);
        assert_eq!(d.shape_ids, ["shape"]);
        assert_eq!(d.severity, her_protocol::deck::DiagnosticSeverity::Error);
        assert!(d.fix.is_none());
    }

    #[test]
    fn real_snapshot_reports_visible_unrotated_shapes_and_missing_picture_text() {
        let deck =
            crate::Deck::open(include_bytes!("../tests/fixtures/editor.pptx").to_vec()).unwrap();
        let mut snapshot = deck.presentation.snapshot().unwrap();
        let slide = &mut snapshot.slides[0];
        slide.hidden = Some(false);
        let shape = &mut slide.shapes[0];
        shape.hidden = false;
        shape.inherited = None;
        shape.rotation_deg = 0.0;
        shape.x = -1;
        shape.y = 0;
        shape.width = 100;
        shape.height = 100;
        shape.kind = ShapeKind::Picture;
        shape.decorative = false;
        shape.title = None;
        shape.description = None;
        let shape_id = shape.id.clone();
        let found = check(&snapshot);
        assert!(
            found
                .iter()
                .any(|d| d.rule == "layout.off_slide" && d.shape_ids == [shape_id.clone()])
        );
        assert!(
            found
                .iter()
                .any(|d| d.rule == "a11y.alt_text" && d.shape_ids == [shape_id.clone()])
        );
        let shape = &mut snapshot.slides[0].shapes[0];
        shape.decorative = true;
        shape.rotation_deg = 45.0;
        let found = check(&snapshot);
        assert!(!found.iter().any(|d| d.shape_ids == [shape_id.clone()]));
    }

    #[test]
    fn actual_deck_snapshot_can_be_checked_without_modification() {
        let deck =
            crate::Deck::open(include_bytes!("../tests/fixtures/editor.pptx").to_vec()).unwrap();
        let first = deck.check().unwrap();
        assert_eq!(first, deck.check().unwrap());
        assert!(
            first
                .iter()
                .all(|d| d.index > 0 && !d.slide_id.is_empty() && !d.shape_ids.is_empty())
        );
    }
}
