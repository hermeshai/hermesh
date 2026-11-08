//! The thin layout analysis on PDFium's characters (design DE-17, §9.2.3): characters into
//! lines, lines into blocks, blocks into headings, body and margins. Also the two ways text is
//! taken by position: a selection (`resolve`) and a search (`find`). Pure: no PDFium here.

use her_protocol::doc::BlockKind;
use her_protocol::doc::Rect;
use her_protocol::doc::TextBlock;
use serde::Deserialize;
use serde::Serialize;

/// One character of a page as PDFium has it, in content order. Spaces PDFium generated between
/// words carry an empty box.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Char {
    pub c: char,
    pub r: Rect,
    /// Font size in points, as scaled on the page.
    pub size: f32,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub bold: bool,
}

impl Char {
    fn is_space(&self) -> bool {
        self.c.is_whitespace()
    }

    fn cx(&self) -> f32 {
        (self.r.x0 + self.r.x1) / 2.0
    }

    fn cy(&self) -> f32 {
        (self.r.y0 + self.r.y1) / 2.0
    }
}

/// The characters of one page, in PDF user space (where anchors are), and how the page shows:
/// its visible box (`crop`, user space) turned by `rotation` degrees clockwise. `width` and
/// `height` are the page as shown.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct PageChars {
    pub width: f32,
    pub height: f32,
    pub chars: Vec<Char>,
    #[serde(default)]
    pub crop: Rect,
    #[serde(default)]
    pub rotation: u16,
}

impl PageChars {
    /// A rectangle of user space as shown: origin at the visible box's bottom left, y up.
    fn to_shown(&self, r: &Rect) -> Rect {
        let c = &self.crop;
        let (ax, ay, bx, by) = match self.rotation {
            90 => (r.y0 - c.y0, c.x1 - r.x0, r.y1 - c.y0, c.x1 - r.x1),
            180 => (c.x1 - r.x0, c.y1 - r.y0, c.x1 - r.x1, c.y1 - r.y1),
            270 => (c.y1 - r.y0, r.x0 - c.x0, c.y1 - r.y1, r.x1 - c.x0),
            _ => (r.x0 - c.x0, r.y0 - c.y0, r.x1 - c.x0, r.y1 - c.y0),
        };
        Rect { x0: ax.min(bx), y0: ay.min(by), x1: ax.max(bx), y1: ay.max(by) }
    }

    /// `to_shown` the other way.
    fn to_user(&self, r: &Rect) -> Rect {
        let c = &self.crop;
        let (ax, ay, bx, by) = match self.rotation {
            90 => (c.x1 - r.y0, r.x0 + c.y0, c.x1 - r.y1, r.x1 + c.y0),
            180 => (c.x1 - r.x0, c.y1 - r.y0, c.x1 - r.x1, c.y1 - r.y1),
            270 => (r.y0 + c.x0, c.y1 - r.x0, r.y1 + c.x0, c.y1 - r.x1),
            _ => (r.x0 + c.x0, r.y0 + c.y0, r.x1 + c.x0, r.y1 + c.y0),
        };
        Rect { x0: ax.min(bx), y0: ay.min(by), x1: ax.max(bx), y1: ay.max(by) }
    }

    /// The page as shown: layout reads it this way, so a page turned on its side reads in lines.
    fn shown(&self) -> PageChars {
        let chars = self.chars.iter().map(|c| Char { r: if c.is_space() { c.r } else { self.to_shown(&c.r) }, ..c.clone() }).collect();
        PageChars { width: self.width, height: self.height, chars, crop: Rect::default(), rotation: 0 }
    }
}

/// CJK ideographs, kana, hangul and their punctuation: written without spaces between them.
pub fn is_cjk(c: char) -> bool {
    matches!(c as u32,
        0x2E80..=0x9FFF | 0xAC00..=0xD7AF | 0xF900..=0xFAFF | 0xFE30..=0xFE4F | 0xFF00..=0xFFEF | 0x20000..=0x2FA1F)
}

/// Whether two boxes sit on one row: they overlap up and down by a good part of the smaller,
/// or their middles are close. Punctuation is short and sits low; it still counts.
fn same_row(a: &Rect, b: &Rect) -> bool {
    let h = a.height().max(b.height()).max(1.0);
    let overlap = a.y1.min(b.y1) - a.y0.max(b.y0);
    overlap > a.height().min(b.height()).max(0.5) * 0.3 || ((a.y0 + a.y1) / 2.0 - (b.y0 + b.y1) / 2.0).abs() < h * 0.5
}

/// A line: indexes into the page's characters, spaces included, in content order.
#[derive(Clone, Debug)]
struct Line {
    chars: Vec<usize>,
    rect: Rect,
    /// The usual font size of the line.
    size: f32,
    bold: bool,
}

/// The lines of a page: a character starts a new line when it is not beside the previous one
/// on the same baseline.
fn lines(page: &PageChars) -> Vec<Line> {
    let mut out: Vec<Line> = Vec::new();
    let mut current: Vec<usize> = Vec::new();
    let mut last: Option<usize> = None;
    // The current line's box, which a character must sit on to join it.
    let mut row: Option<Rect> = None;
    for (i, ch) in page.chars.iter().enumerate() {
        if ch.is_space() {
            if !current.is_empty() {
                current.push(i);
            }
            continue;
        }
        if let (Some(prev), Some(r)) = (last.map(|p| &page.chars[p]), row) {
            let h = prev.r.height().max(ch.r.height()).max(1.0);
            // Forward on the row, or a little back (kerning, overprinted accents).
            let forward = ch.r.x0 >= prev.r.x0 - h * 0.3;
            let near = ch.r.x0 - prev.r.x1 < h * 6.0;
            if !(same_row(&r, &ch.r) && forward && near) {
                out.extend(finish(page, std::mem::take(&mut current)));
                row = None;
            }
        }
        current.push(i);
        row = Some(row.map_or(ch.r, |r| r.union(&ch.r)));
        last = Some(i);
    }
    out.extend(finish(page, current));
    out
}

fn finish(page: &PageChars, mut chars: Vec<usize>) -> Option<Line> {
    while chars.last().is_some_and(|&i| page.chars[i].is_space()) {
        chars.pop();
    }
    let visible: Vec<&Char> = chars.iter().map(|&i| &page.chars[i]).filter(|c| !c.is_space()).collect();
    let first = visible.first()?;
    let rect = visible.iter().fold(first.r, |r, c| r.union(&c.r));
    let mut sizes: Vec<f32> = visible.iter().map(|c| c.size).collect();
    sizes.sort_by(f32::total_cmp);
    let size = sizes[sizes.len() / 2];
    let bold = visible.iter().filter(|c| c.bold).count() * 2 > visible.len();
    Some(Line { chars, rect, size, bold })
}

/// The text of some characters, in order: a space where PDFium put one, or where a gap between
/// two non-CJK characters is as wide as one.
fn join_chars(page: &PageChars, chars: &[usize]) -> String {
    let mut out = String::new();
    let mut prev: Option<&Char> = None;
    let mut pending_space = false;
    for &i in chars {
        let ch = &page.chars[i];
        if ch.is_space() {
            pending_space = prev.is_some();
            continue;
        }
        if let Some(p) = prev {
            let gap = ch.r.x0 - p.r.x1;
            let wide = gap > p.size.max(ch.size) * 0.25;
            let cjk = is_cjk(p.c) || is_cjk(ch.c);
            if (pending_space && !(cjk && !wide)) || (wide && !cjk) {
                out.push(' ');
            }
        }
        out.push(ch.c);
        prev = Some(ch);
        pending_space = false;
    }
    out
}

/// Texts that follow one another (a selection's pages) as one, joined as lines are.
pub fn join_texts(texts: &[String]) -> String {
    join_lines(texts)
}

/// Lines into one text: nothing between CJK lines, a word broken by a hyphen put back together,
/// a space otherwise.
fn join_lines(texts: &[String]) -> String {
    let mut out = String::new();
    for text in texts {
        if out.is_empty() {
            out.push_str(text);
            continue;
        }
        let end = out.chars().last().unwrap_or(' ');
        let start = text.chars().next().unwrap_or(' ');
        if end == '-' && start.is_lowercase() && out.chars().rev().nth(1).is_some_and(char::is_alphabetic) {
            out.pop();
        } else if !(is_cjk(end) || is_cjk(start)) {
            out.push(' ');
        }
        out.push_str(text);
    }
    out
}

/// A run of lines that reads as one: a column, a vertical run of single characters.
struct Group {
    lines: Vec<Line>,
    rect: Rect,
    vertical: bool,
}

/// Lines into blocks: the next line joins a block when it sits right under the block's last
/// line, overlaps it across, and has about the same size.
fn group(lines: Vec<Line>) -> Vec<Group> {
    let mut groups: Vec<Group> = Vec::new();
    for line in lines {
        if let Some(g) = groups.last_mut() {
            let last = g.lines.last().expect("groups are never empty");
            let h = last.rect.height().max(line.rect.height()).max(1.0);
            let gap = last.rect.y0 - line.rect.y1;
            let overlap = line.rect.x0 < g.rect.x1 + h && g.rect.x0 < line.rect.x1 + h;
            let same_size = (line.size - last.size).abs() <= last.size.max(line.size) * 0.2;
            let single = |l: &Line| l.rect.width() < l.size * 1.6;
            // Upright characters stacked down the margin, one a line.
            let vertical_step = single(last) && single(&line) && (line.rect.x0 - last.rect.x0).abs() < h * 0.5 && gap > -h * 0.2 && gap < h * 1.5;
            if vertical_step && (g.vertical || g.lines.len() == 1) {
                g.vertical = true;
                g.rect = g.rect.union(&line.rect);
                g.lines.push(line);
                continue;
            }
            if !g.vertical && gap > -h * 0.3 && gap < h * 1.0 && overlap && same_size && line.bold == last.bold {
                g.rect = g.rect.union(&line.rect);
                g.lines.push(line);
                continue;
            }
        }
        groups.push(Group { rect: line.rect, vertical: false, lines: vec![line] });
    }
    groups
}

/// The page's blocks, laid out as the page shows; their rectangles in user space.
pub fn blocks(page_no: u32, page: &PageChars) -> Vec<TextBlock> {
    let mut blocks = shown_blocks(page_no, &page.shown());
    for b in &mut blocks {
        b.rect = page.to_user(&b.rect);
    }
    blocks
}

/// The page's blocks, with their kinds and short ids (`p3.b0`…), in reading order: the body as
/// PDFium has the content, margins at the end.
fn shown_blocks(page_no: u32, page: &PageChars) -> Vec<TextBlock> {
    let groups = group(lines(page));
    let body_size = body_size(&groups);
    let mut body = Vec::new();
    let mut margins = Vec::new();
    for g in &groups {
        let texts: Vec<String> = g.lines.iter().map(|l| join_chars(page, &l.chars)).collect();
        let text = if g.vertical { texts.concat() } else { join_lines(&texts) };
        if text.trim().is_empty() {
            continue;
        }
        let kind = kind_of(g, &text, page, body_size);
        let block = (kind, g.rect, text);
        if kind == BlockKind::Margin { margins.push(block) } else { body.push(block) }
    }
    body.into_iter()
        .chain(margins)
        .enumerate()
        .map(|(i, (kind, rect, text))| TextBlock {
            id: format!("p{page_no}.b{i}"),
            page: page_no,
            kind,
            rect,
            text,
        })
        .collect()
}

/// The font size most of the page's text is in.
fn body_size(groups: &[Group]) -> f32 {
    let mut weights: Vec<(f32, usize)> = Vec::new();
    for line in groups.iter().flat_map(|g| &g.lines) {
        let size = (line.size * 2.0).round() / 2.0;
        match weights.iter_mut().find(|(s, _)| *s == size) {
            Some((_, n)) => *n += line.chars.len(),
            None => weights.push((size, line.chars.len())),
        }
    }
    weights.into_iter().max_by_key(|(_, n)| *n).map(|(s, _)| s).unwrap_or(10.0)
}

fn kind_of(g: &Group, text: &str, page: &PageChars, body_size: f32) -> BlockKind {
    if g.vertical {
        return BlockKind::Margin;
    }
    // Running heads, footers and page numbers: a short block in the top or bottom 6% of the page.
    let band = page.height * 0.06;
    let short = text.chars().count() <= 60 && g.lines.len() <= 2;
    if page.height > 0.0 && short && (g.rect.y0 > page.height - band || g.rect.y1 < band) {
        return BlockKind::Margin;
    }
    let size = g.lines.iter().map(|l| l.size).fold(0.0, f32::max);
    let few = g.lines.len() <= 3 && text.chars().count() <= 120;
    if few && (size >= body_size * 1.25 || (g.lines.iter().all(|l| l.bold) && size >= body_size && text.chars().count() <= 40)) {
        return BlockKind::Heading;
    }
    BlockKind::Body
}

/// The page as Markdown for the agent: headings, paragraphs, and the margin text in one line
/// at the end.
pub fn markdown(blocks: &[TextBlock]) -> String {
    let mut out = String::new();
    let mut margins = Vec::new();
    for b in blocks {
        match b.kind {
            BlockKind::Heading => out.push_str(&format!("## {}\n\n", b.text)),
            BlockKind::Body => out.push_str(&format!("{}\n\n", b.text)),
            BlockKind::Margin => margins.push(b.text.as_str()),
        }
    }
    if !margins.is_empty() {
        out.push_str(&format!("(margins: {})\n", margins.join(" · ")));
    }
    out
}

/// What a selection takes: the characters whose middle falls in one of `rects`, as text, the
/// rectangle of each line, and the boxes of the characters.
pub struct Taken {
    pub text: String,
    pub lines: Vec<Rect>,
    pub boxes: Vec<Rect>,
    /// The first character taken, an index into the page's characters.
    pub first: Option<usize>,
}

/// What `rects` (user space) take of the page; lines and boxes in user space.
pub fn take(page: &PageChars, rects: &[Rect]) -> Taken {
    let shown: Vec<Rect> = rects.iter().map(|r| page.to_shown(r)).collect();
    let mut taken = shown_take(&page.shown(), &shown);
    taken.lines = taken.lines.iter().map(|r| page.to_user(r)).collect();
    taken.boxes = taken.boxes.iter().map(|r| page.to_user(r)).collect();
    taken
}

fn shown_take(page: &PageChars, rects: &[Rect]) -> Taken {
    let inside = |c: &Char| rects.iter().any(|r| r.contains(c.cx(), c.cy()));
    let mut texts = Vec::new();
    let mut line_rects = Vec::new();
    let mut boxes = Vec::new();
    let mut first = None;
    for line in lines(page) {
        // Spaces between two taken characters belong to the selection.
        let picked: Vec<usize> = line.chars.iter().copied().filter(|&i| !page.chars[i].is_space() && inside(&page.chars[i])).collect();
        let (Some(&a), Some(&b)) = (picked.first(), picked.last()) else { continue };
        first.get_or_insert(a);
        let span: Vec<usize> = line.chars.iter().copied().filter(|&i| i >= a && i <= b && (page.chars[i].is_space() || picked.contains(&i))).collect();
        texts.push(join_chars(page, &span));
        let rect = picked.iter().map(|&i| page.chars[i].r).reduce(|a, b| a.union(&b)).expect("picked is not empty");
        line_rects.push(rect);
        boxes.extend(picked.iter().map(|&i| page.chars[i].r));
    }
    Taken { text: join_lines(&texts), lines: line_rects, boxes, first }
}

/// Text compared loosely (design §9.4.1): compatibility forms folded, case ignored, whitespace
/// and soft hyphens dropped. Returns the folded characters with the index each came from.
pub fn fold<'a>(chars: impl Iterator<Item = (usize, char)> + 'a) -> Vec<(usize, char)> {
    let mut out = Vec::new();
    for (i, c) in chars {
        if c.is_whitespace() || c == '\u{00AD}' {
            continue;
        }
        let c = match c as u32 {
            // Full-width ASCII forms to ASCII.
            0xFF01..=0xFF5E => char::from_u32(c as u32 - 0xFEE0).unwrap_or(c),
            _ => c,
        };
        match c {
            'ﬁ' => out.extend([(i, 'f'), (i, 'i')]),
            'ﬂ' => out.extend([(i, 'f'), (i, 'l')]),
            'ﬀ' => out.extend([(i, 'f'), (i, 'f')]),
            'ﬃ' => out.extend([(i, 'f'), (i, 'f'), (i, 'i')]),
            'ﬄ' => out.extend([(i, 'f'), (i, 'f'), (i, 'l')]),
            '–' | '—' | '−' | '‐' | '‑' => out.push((i, '-')),
            '“' | '”' => out.push((i, '"')),
            '‘' | '’' => out.push((i, '\'')),
            _ => out.extend(c.to_lowercase().map(|l| (i, l))),
        }
    }
    out
}

/// Where `query` occurs on a page: for each hit, the indexes of its first and last character.
pub fn find(page: &PageChars, query: &str) -> Vec<(usize, usize)> {
    let needle: Vec<char> = fold(query.chars().enumerate()).into_iter().map(|(_, c)| c).collect();
    if needle.is_empty() {
        return Vec::new();
    }
    let hay = fold(page.chars.iter().map(|c| c.c).enumerate());
    let mut hits = Vec::new();
    let mut at = 0;
    while at + needle.len() <= hay.len() {
        if hay[at..at + needle.len()].iter().map(|(_, c)| *c).eq(needle.iter().copied()) {
            hits.push((hay[at].0, hay[at + needle.len() - 1].0));
            at += needle.len();
        } else {
            at += 1;
        }
    }
    hits
}

/// The rectangles of characters `a..=b`, one a line, in user space.
pub fn span_rects(page: &PageChars, a: usize, b: usize) -> Vec<Rect> {
    shown_span_rects(&page.shown(), a, b).iter().map(|r| page.to_user(r)).collect()
}

/// `span_rects` on the page as shown.
fn shown_span_rects(page: &PageChars, a: usize, b: usize) -> Vec<Rect> {
    let mut out: Vec<Rect> = Vec::new();
    let mut last: Option<&Char> = None;
    for ch in page.chars[a..=b].iter().filter(|c| !c.is_space()) {
        match (out.last_mut(), last) {
            (Some(r), Some(_)) if same_row(r, &ch.r) => *r = r.union(&ch.r),
            _ => out.push(ch.r),
        }
        last = Some(ch);
    }
    out
}

/// The block holding character `i`: the first block whose rectangle has its middle.
pub fn block_at<'a>(blocks: &'a [TextBlock], page: &PageChars, i: usize) -> Option<&'a TextBlock> {
    let c = page.chars.get(i)?;
    blocks.iter().find(|b| b.rect.contains(c.cx(), c.cy()))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A line of text set from `x`, `y` (the baseline), in `size` points, one advance a char.
    fn line(chars: &mut Vec<Char>, text: &str, x: f32, y: f32, size: f32) {
        let mut x = x;
        for c in text.chars() {
            let w = if is_cjk(c) { size } else { size * 0.5 };
            if c == ' ' {
                chars.push(Char { c, r: Rect::default(), size, bold: false });
            } else {
                chars.push(Char { c, r: Rect { x0: x, y0: y, x1: x + w, y1: y + size * 0.8 }, size, bold: false });
            }
            x += w;
        }
    }

    fn page(build: impl FnOnce(&mut Vec<Char>)) -> PageChars {
        let mut chars = Vec::new();
        build(&mut chars);
        PageChars { width: 600.0, height: 800.0, chars, ..PageChars::default() }
    }

    #[test]
    fn paragraphs_headings_and_margins() {
        let p = page(|c| {
            line(c, "投资要点", 50.0, 700.0, 18.0);
            line(c, "维持在 20-25%之间，", 50.0, 660.0, 10.0);
            line(c, "其中打新贡献在 5%-10%。", 50.0, 647.0, 10.0);
            line(c, "Net income rose by a sig-", 50.0, 600.0, 10.0);
            line(c, "nificant margin.", 50.0, 587.0, 10.0);
            line(c, "第 1 页", 280.0, 20.0, 9.0);
            for (k, ch) in "证券研究报告".chars().enumerate() {
                line(c, &ch.to_string(), 560.0, 500.0 - k as f32 * 12.0, 10.0);
            }
        });
        let blocks = blocks(1, &p);
        let view: Vec<(BlockKind, &str)> = blocks.iter().map(|b| (b.kind, b.text.as_str())).collect();
        assert_eq!(
            view,
            vec![
                (BlockKind::Heading, "投资要点"),
                (BlockKind::Body, "维持在 20-25%之间，其中打新贡献在 5%-10%。"),
                (BlockKind::Body, "Net income rose by a significant margin."),
                (BlockKind::Margin, "第 1 页"),
                (BlockKind::Margin, "证券研究报告"),
            ]
        );
        assert_eq!(blocks[0].id, "p1.b0");
        assert_eq!(blocks[4].id, "p1.b4");
        let md = markdown(&blocks);
        assert!(md.starts_with("## 投资要点\n\n维持在 20-25%之间"), "{md}");
        assert!(md.ends_with("(margins: 第 1 页 · 证券研究报告)\n"), "{md}");
    }

    #[test]
    fn a_selection_takes_the_characters_inside() {
        let p = page(|c| {
            line(c, "违约金为合同总额的 20%，", 50.0, 660.0, 10.0);
            line(c, "逾期另计。", 50.0, 647.0, 10.0);
        });
        // From "合同" on the first line to the end of the second.
        let rects = [
            Rect { x0: 90.0, y0: 658.0, x1: 400.0, y1: 670.0 },
            Rect { x0: 50.0, y0: 645.0, x1: 400.0, y1: 656.0 },
        ];
        let taken = take(&p, &rects);
        assert_eq!(taken.text, "合同总额的 20%，逾期另计。");
        assert_eq!(taken.lines.len(), 2);
        assert_eq!(taken.boxes.len(), "合同总额的20%，逾期另计。".chars().count());
    }

    #[test]
    fn search_is_loose_about_spaces_case_and_ligatures() {
        let p = page(|c| {
            line(c, "The ﬁrst Quarter", 50.0, 700.0, 10.0);
            line(c, "营业 收入 12.5%", 50.0, 680.0, 10.0);
        });
        assert_eq!(find(&p, "first quarter").len(), 1);
        assert_eq!(find(&p, "营业收入").len(), 1);
        let (a, b) = find(&p, "收入12.5％")[0];
        assert_eq!(p.chars[a].c, '收');
        assert_eq!(p.chars[b].c, '%');
        assert!(find(&p, "").is_empty());
        let (a, b) = find(&p, "quarter 营业")[0];
        assert_eq!(span_rects(&p, a, b).len(), 2);
    }
}
