//! Complex files from a seed: the encodings read knows and some it does not, every kind of line
//! break, characters from many scripts and control characters, sizes at the boundaries the tools
//! care about, long lines, repeated blocks and source code in several languages.

use rand::Rng;
use rand::SeedableRng;
use rand::rngs::SmallRng;
use rand::seq::IndexedRandom;

pub const KIB: usize = 1024;
pub const MIB: usize = 1024 * 1024;
pub const UTF8_BOM: &[u8] = b"\xEF\xBB\xBF";

/// The generator of a seed. A small one: in debug builds the standard one would take most of
/// the time of the suite.
pub fn rng(seed: u64) -> SmallRng {
    SmallRng::seed_from_u64(seed)
}

/// A generated file.
#[derive(Clone)]
pub struct Sample {
    pub name: String,
    pub bytes: Vec<u8>,
}

impl Sample {
    pub fn new(name: impl Into<String>, bytes: impl Into<Vec<u8>>) -> Self {
        Self {
            name: name.into(),
            bytes: bytes.into(),
        }
    }

    /// The text, when the file is UTF-8 (a byte order mark included).
    pub fn utf8(&self) -> Option<&str> {
        std::str::from_utf8(&self.bytes).ok()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Breaks {
    Lf,
    Crlf,
    Cr,
    /// Each line picks one.
    Mixed,
}

impl Breaks {
    pub const ALL: [Breaks; 4] = [Breaks::Lf, Breaks::Crlf, Breaks::Cr, Breaks::Mixed];

    pub fn pick(self, rng: &mut SmallRng) -> &'static str {
        match self {
            Breaks::Lf => "\n",
            Breaks::Crlf => "\r\n",
            Breaks::Cr => "\r",
            Breaks::Mixed => ["\n", "\n", "\r\n", "\r"][rng.random_range(0..4)],
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Breaks::Lf => "lf",
            Breaks::Crlf => "crlf",
            Breaks::Cr => "cr",
            Breaks::Mixed => "mixed",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Encoding {
    Utf8,
    Utf8Bom,
    Utf16Le,
    Utf16Be,
    /// Non-ASCII characters as single bytes 0x80-0xFF: mostly invalid UTF-8.
    Latin1,
    /// UTF-8 with invalid sequences mixed in: stray continuation bytes, cut sequences, overlong
    /// forms, encoded surrogates, 0xFE and 0xFF.
    BrokenUtf8,
}

impl Encoding {
    pub const ALL: [Encoding; 6] = [
        Encoding::Utf8,
        Encoding::Utf8Bom,
        Encoding::Utf16Le,
        Encoding::Utf16Be,
        Encoding::Latin1,
        Encoding::BrokenUtf8,
    ];

    pub fn name(self) -> &'static str {
        match self {
            Encoding::Utf8 => "utf8",
            Encoding::Utf8Bom => "utf8bom",
            Encoding::Utf16Le => "utf16le",
            Encoding::Utf16Be => "utf16be",
            Encoding::Latin1 => "latin1",
            Encoding::BrokenUtf8 => "broken",
        }
    }
}

/// Pieces of text that put every kind of character in the corpus.
pub const ATOMS: &[&str] = &[
    // Scripts.
    "日本語のテキスト",
    "中文字符",
    "한국어",
    "Ελληνικά",
    "Кириллица",
    "ไทย",
    "café",
    // Emoji, ZWJ sequences, flags and skin tones.
    "😀",
    "👨\u{200D}👩\u{200D}👧\u{200D}👦",
    "🏳\u{FE0F}\u{200D}🌈",
    "👍🏽",
    "🇯🇵",
    "🧑🏾\u{200D}💻",
    // Combining marks.
    "e\u{301}",
    "a\u{308}\u{304}",
    "Z\u{36B}\u{36F}\u{344}\u{35C}",
    // Right to left, with direction marks and an override.
    "שלום עולם",
    "مرحبا بالعالم",
    "\u{200F}",
    "\u{200E}",
    "\u{202E}txet\u{202C}",
    // Zero-width characters.
    "\u{200B}",
    "\u{200C}",
    "\u{200D}",
    "\u{2060}",
    "\u{FEFF}",
    // Unicode spaces.
    "\u{A0}",
    "\u{2002}",
    "\u{2003}",
    "\u{2009}",
    "\u{202F}",
    "\u{205F}",
    "\u{3000}",
    // Typographic quotes and dashes.
    "“quoted”",
    "‘single’",
    "—",
    "–",
    "−",
    "‐",
    "don’t",
    "„low”",
    // Escape sequences and other control characters; none of them ends a line.
    "\x1b[31mred\x1b[0m",
    "\x1b]0;title\x07",
    "\x1b[2K\x1b[1G",
    "\x07",
    "\x08",
    "\x0b",
    "\x0c",
    "\x7f",
    "\x01",
    "\u{85}",
    "\u{2028}",
    "\u{2029}",
    // Whitespace.
    "\t",
    "  ",
    " \t ",
];

const WORDS: &[&str] = &[
    "alpha", "beta", "gamma", "delta", "value", "result", "config", "buffer", "index", "token", "parse", "render",
    "the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "x", "y", "i", "n", "0", "42", "3.14", "-1", "{",
    "}", "(", ")", "[", "]", ";", ",", ".", "=", "==", "=>", "->", "::", "/*", "*/", "//", "#", "\"", "'", "`", "\\",
];

/// One line of mixed text: indentation, words and atoms, sometimes trailing whitespace.
pub fn mixed_line(rng: &mut SmallRng, max_items: usize) -> String {
    let mut line = String::new();
    match rng.random_range(0..6) {
        0 => line.push('\t'),
        1 => line.push_str("    "),
        2 => line.push_str("        "),
        _ => {}
    }
    let items = rng.random_range(0..=max_items);
    for i in 0..items {
        if i > 0 {
            line.push(' ');
        }
        if rng.random_bool(0.3) {
            line.push_str(ATOMS.choose(rng).unwrap());
        } else {
            line.push_str(WORDS.choose(rng).unwrap());
        }
    }
    match rng.random_range(0..8) {
        0 => line.push(' '),
        1 => line.push('\t'),
        2 => line.push_str(" \t "),
        _ => {}
    }
    line
}

/// Lines of mixed text, at least `size` bytes of UTF-8, each ended per `breaks`.
pub fn mixed_text(rng: &mut SmallRng, size: usize, breaks: Breaks) -> String {
    let mut text = String::with_capacity(size + 256);
    while text.len() < size {
        let line = if rng.random_ratio(1, 25) { String::new() } else { mixed_line(rng, 12) };
        text.push_str(&line);
        text.push_str(breaks.pick(rng));
    }
    text
}

/// `text` (line breaks as `\n`) with each line break written per `breaks`.
pub fn with_breaks(text: &str, breaks: Breaks, rng: &mut SmallRng) -> String {
    if breaks == Breaks::Lf {
        return text.to_string();
    }
    let mut out = String::with_capacity(text.len() + text.len() / 16);
    for piece in text.split_inclusive('\n') {
        match piece.strip_suffix('\n') {
            Some(line) => {
                out.push_str(line);
                out.push_str(breaks.pick(rng));
            }
            None => out.push_str(piece),
        }
    }
    out
}

/// `text` in `encoding`.
pub fn encode(text: &str, encoding: Encoding, rng: &mut SmallRng) -> Vec<u8> {
    match encoding {
        Encoding::Utf8 => text.as_bytes().to_vec(),
        Encoding::Utf8Bom => [UTF8_BOM, text.as_bytes()].concat(),
        Encoding::Utf16Le => [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect(),
        Encoding::Utf16Be => [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect(),
        Encoding::Latin1 => text
            .chars()
            .map(|c| if c.is_ascii() { c as u8 } else { 0x80 | (c as u32 % 0x80) as u8 })
            .collect(),
        Encoding::BrokenUtf8 => {
            const BROKEN: [&[u8]; 9] = [
                b"\x80",
                b"\xBF\xBF",
                b"\xE6\x97",
                b"\xF0\x9F\x98",
                b"\xC0\xAF",
                b"\xED\xA0\x80",
                b"\xF4\x90\x80\x80",
                b"\xFE",
                b"\xFF",
            ];
            let mut out = Vec::with_capacity(text.len() + text.len() / 32);
            // Never first: 0xFE or 0xFF there could make a UTF-16 byte order mark.
            let mut at = 0;
            while at < text.len() {
                let mut next = (at + rng.random_range(1..80)).min(text.len());
                while !text.is_char_boundary(next) {
                    next += 1;
                }
                out.extend_from_slice(&text.as_bytes()[at..next]);
                if next < text.len() {
                    out.extend_from_slice(BROKEN.choose(rng).unwrap());
                }
                at = next;
            }
            out
        }
    }
}

/// The first `size` bytes of `bytes`, padded with `x`: the cut may split a character or a CRLF.
pub fn fit_raw(mut bytes: Vec<u8>, size: usize) -> Vec<u8> {
    bytes.resize(size, b'x');
    bytes
}

/// `text` cut at a character boundary before `size` bytes and padded with `x` to exactly `size`
/// bytes. A cut never leaves a `\r` last, so whatever follows cannot join it into a CRLF.
pub fn fit_utf8(text: &str, size: usize) -> Vec<u8> {
    let mut end = size.min(text.len());
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    let mut out = text.as_bytes()[..end].to_vec();
    if let Some(last) = out.last_mut()
        && *last == b'\r'
    {
        *last = b'x';
    }
    out.resize(size, b'x');
    out
}

/// Mixed text with `piece` starting exactly at byte `at`, `size` bytes in all.
pub fn piece_at(rng: &mut SmallRng, at: usize, piece: &[u8], size: usize, breaks: Breaks) -> Vec<u8> {
    let mut out = fit_utf8(&mixed_text(rng, at, breaks), at);
    out.extend_from_slice(piece);
    let rest = size.saturating_sub(out.len());
    let tail = mixed_text(rng, rest, breaks);
    // An `x` first, so that a CR at the end of `piece` stays a lone CR.
    out.extend_from_slice(&fit_utf8(&format!("x{tail}"), rest));
    out
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lang {
    Rust,
    Python,
    JavaScript,
    Go,
    C,
    Html,
    Json,
    Yaml,
    Markdown,
    Shell,
}

impl Lang {
    pub const ALL: [Lang; 10] = [
        Lang::Rust,
        Lang::Python,
        Lang::JavaScript,
        Lang::Go,
        Lang::C,
        Lang::Html,
        Lang::Json,
        Lang::Yaml,
        Lang::Markdown,
        Lang::Shell,
    ];

    pub fn extension(self) -> &'static str {
        match self {
            Lang::Rust => "rs",
            Lang::Python => "py",
            Lang::JavaScript => "js",
            Lang::Go => "go",
            Lang::C => "c",
            Lang::Html => "html",
            Lang::Json => "json",
            Lang::Yaml => "yaml",
            Lang::Markdown => "md",
            Lang::Shell => "sh",
        }
    }
}

const NAMES: &[&str] = &[
    "parse", "load", "store", "render", "update", "compute", "fetch", "encode", "decode", "merge", "split", "check",
];
const NOUNS: &[&str] = &["config", "buffer", "token", "user", "record", "line", "frame", "entry", "node", "path"];
const STRINGS: &[&str] =
    &["日本語", "naïve café", "“smart” quotes", "emoji 😀👍🏽", "tab\there", "dash — and – more", "שלום", "\u{A0}nbsp"];

/// Plausible source code of at least `size` bytes, line breaks per `breaks`.
pub fn source(rng: &mut SmallRng, lang: Lang, size: usize, breaks: Breaks) -> String {
    let mut text = String::with_capacity(size + 512);
    match lang {
        Lang::Html => text.push_str("<!DOCTYPE html>\n<html lang=\"en\">\n<body>\n"),
        Lang::Json => text.push_str("[\n"),
        Lang::Shell => text.push_str("#!/bin/sh\nset -eu\n\n"),
        Lang::Go => text.push_str("package main\n\nimport \"fmt\"\n\n"),
        Lang::C => text.push_str("#include <string.h>\n#include <stddef.h>\n\n"),
        _ => {}
    }
    let mut n = 0;
    while text.len() < size {
        n += 1;
        let name = format!("{}_{}{n}", NAMES.choose(rng).unwrap(), NOUNS.choose(rng).unwrap());
        let arg = *NOUNS.choose(rng).unwrap();
        let s = *STRINGS.choose(rng).unwrap();
        let k = rng.random_range(1..1000);
        let unit = match lang {
            Lang::Rust => format!(
                "/// {name}: “{s}” — returns the value.\npub fn {name}({arg}: &str) -> Result<u32, Error> {{\n    let \
                 value = {arg}.len() as u32; // {s}\n    if value > {k} {{\n        return Err(Error::new(\"{name} → \
                 {s}\"));\n    }}\n    Ok(value * {k})\n}}\n\n"
            ),
            Lang::Python => format!(
                "def {name}({arg}, *args, **kwargs):\n    \"\"\"{s}: “docstring”.\"\"\"\n    if {arg} is None:\n        \
                 return {k}\n    for item in args:\n        print(f\"{{item}}\\t{s}\")\n    return {arg} + {k}\n\n\n"
            ),
            Lang::JavaScript => format!(
                "export function {name}({arg}) {{\n  const value = `${{{arg}}} {s}`;\n  return value.length > {k} ? \
                 value : null; // {name}\n}}\n\n"
            ),
            Lang::Go => format!(
                "func {name}({arg} string) (int, error) {{\n\tif len({arg}) == 0 {{\n\t\treturn 0, \
                 fmt.Errorf(\"{s}: %q\", {arg})\n\t}}\n\treturn len({arg}) + {k}, nil\n}}\n\n"
            ),
            Lang::C => format!(
                "static int {name}(const char *{arg}) {{\n    /* {s} */\n    if ({arg} == NULL) {{\n        return \
                 -{k};\n    }}\n    return (int)strlen({arg}) * {k};\n}}\n\n"
            ),
            Lang::Html => format!(
                "<div class=\"{arg}\" id=\"{name}\">\n  <p title=\"{s}\">{s} &amp; {arg}</p>\n  <a \
                 href=\"/{name}\">{name} →</a>\n</div>\n"
            ),
            Lang::Json => format!(
                "  {{\"id\": {n}, \"name\": \"{name}\", \"tags\": [\"{s}\", \"{arg}\"], \"value\": {k}.5}},\n"
            ),
            Lang::Yaml => {
                format!("{name}:\n  id: {n}\n  title: \"{s}\"\n  items:\n    - {arg}\n    - {k}\n  enabled: true\n")
            }
            Lang::Markdown => format!(
                "## {name} {n}\n\nSome *{arg}* text with `code` and “quotes” — {s}.  \n\n- item {arg}\n- item \
                 {k}\n\n"
            ),
            Lang::Shell => format!(
                "{name}() {{\n  local {arg}=\"$1\"\n  if [ -z \"${arg}\" ]; then\n    echo \"{s}\" >&2\n    return \
                 1\n  fi\n  printf '%s\\t%s\\n' \"${arg}\" \"{k}\"\n}}\n\n"
            ),
        };
        text.push_str(&unit);
    }
    match lang {
        Lang::Html => text.push_str("</body>\n</html>\n"),
        Lang::Json => text.push_str("  {}\n]\n"),
        _ => {}
    }
    with_breaks(&text, breaks, rng)
}

/// Minified JavaScript: one line of at least `size` bytes.
pub fn minified(rng: &mut SmallRng, size: usize) -> String {
    let mut line = String::with_capacity(size + 64);
    let mut i = 0;
    while line.len() < size {
        i += 1;
        let s = STRINGS.choose(rng).unwrap().replace('\t', " ");
        line.push_str(&format!("function a{i}(b){{return b*{i}+\"{s}\"}};var c{i}=a{i}({i});"));
    }
    line
}

/// A block of code repeated `times`, now and then with one line changed.
pub fn repeated_blocks(rng: &mut SmallRng, times: usize, breaks: Breaks) -> String {
    let block = "    if (ready) {\n        flush(buffer);\n    }\n    count += 1;\n";
    let mut text = String::from("fn main() {\n");
    for i in 0..times {
        if rng.random_ratio(1, 5) {
            text.push_str(&block.replace("count += 1", &format!("count += {}", i + 2)));
        } else {
            text.push_str(block);
        }
        if rng.random_ratio(1, 3) {
            text.push('\n');
        }
    }
    text.push_str("}\n");
    with_breaks(&text, breaks, rng)
}

/// The files of the corpus.
pub fn samples(seed: u64) -> Vec<Sample> {
    let mut rng = rng(seed);
    let rng = &mut rng;
    let mut out = Vec::new();

    // The smallest files, and files of line breaks only.
    for (name, bytes) in [
        ("empty", &b""[..]),
        ("one-byte", b"a"),
        ("lf", b"\n"),
        ("cr", b"\r"),
        ("crlf", b"\r\n"),
        ("lf-cr", b"\n\r"),
        ("cr-cr-lf-lf", b"\r\r\n\n"),
        ("bom", UTF8_BOM),
        ("bom-lf", b"\xEF\xBB\xBF\n"),
        ("bom-zwnbsp", "\u{FEFF}\u{FEFF}x\n".as_bytes()),
        ("utf16le-bom", b"\xFF\xFE"),
        ("utf16be-bom", b"\xFE\xFF"),
        ("utf16le-odd", b"\xFF\xFEa\x00b"),
        ("utf16le-surrogates", b"\xFF\xFE\x3D\xD8a\x00\x00\xDC\n\x00\x3D\xD8\x00\xDE"),
        ("spaces", b"   \t  \n\t\t\n  "),
        ("not-breaks", "a\u{85}b\u{2028}c\u{2029}d\x0be\x0cf\n".as_bytes()),
        ("no-final-break", b"one\ntwo"),
    ] {
        out.push(Sample::new(format!("tiny/{name}.txt"), bytes));
    }
    for breaks in Breaks::ALL {
        let mut text = String::new();
        for _ in 0..rng.random_range(300..700) {
            text.push_str(breaks.pick(rng));
        }
        out.push(Sample::new(format!("breaks/only-{}.txt", breaks.name()), text));
    }
    out.push(Sample::new("breaks/lone-crs.txt", "\r".repeat(1000)));

    // Every encoding with every kind of line break, with and without a break at the end.
    for encoding in Encoding::ALL {
        for breaks in Breaks::ALL {
            for last in [true, false] {
                let size = rng.random_range(2 * KIB..6 * KIB);
                let mut text = mixed_text(rng, size, breaks);
                if !last {
                    text.truncate(text.trim_end_matches(['\r', '\n']).len());
                }
                let name = format!("enc/{}-{}{}.txt", encoding.name(), breaks.name(), if last { "" } else { "-open" });
                out.push(Sample::new(name, encode(&text, encoding, rng)));
            }
        }
    }

    // Sizes at the boundaries: the binary sniff, the first read, the streaming buffer. A cut
    // there may split a character or a CRLF.
    for size in [8 * KIB - 1, 8 * KIB, 8 * KIB + 1, 64 * KIB - 1, 64 * KIB, 64 * KIB + 1, MIB - 1, MIB, MIB + 1] {
        let text = mixed_text(rng, size, Breaks::Mixed);
        out.push(Sample::new(format!("size/mixed-{size}.txt"), fit_raw(text.into_bytes(), size)));
    }
    for breaks in [Breaks::Crlf, Breaks::Cr] {
        for size in [64 * KIB, MIB] {
            let text = mixed_text(rng, size, breaks);
            out.push(Sample::new(format!("size/{}-{size}.txt", breaks.name()), fit_utf8(&text, size)));
        }
    }

    // Line breaks and characters across the buffer boundaries of a streaming read (64 KiB for
    // the first read, 1 MiB buffers and the checkpoints of the line index, 128 KiB pieces of a
    // read that resumes from one).
    let placements: [(&str, usize, &[u8], usize, Breaks); 11] = [
        ("crlf-at-64k", 64 * KIB - 1, b"\r\n", 64 * KIB + 900, Breaks::Lf),
        ("crlf-at-1m", MIB - 1, b"\r\n", MIB + 64 * KIB, Breaks::Crlf),
        ("crlf-at-2m", 2 * MIB - 1, b"\r\n", 2 * MIB + 64 * KIB, Breaks::Mixed),
        ("crlf-at-1m-128k", MIB + 128 * KIB - 1, b"\r\n", MIB + 200 * KIB, Breaks::Crlf),
        ("cr-at-1m", MIB - 1, b"\ra", MIB + 64 * KIB, Breaks::Cr),
        ("lf-at-1m", MIB - 1, b"\nstart of a line\n", MIB + 64 * KIB, Breaks::Lf),
        ("empty-lines-at-1m", MIB - 2, b"\n\n\n\r\n\r", MIB + 64 * KIB, Breaks::Mixed),
        ("emoji-at-1m", MIB - 2, "😀👨\u{200D}👩\u{200D}👧".as_bytes(), MIB + 64 * KIB, Breaks::Lf),
        ("cjk-at-64k", 64 * KIB - 1, "日本語".as_bytes(), 64 * KIB + 900, Breaks::Crlf),
        ("long-line-at-1m", MIB - 100 * KIB, &[b'y'; 200 * KIB], MIB + 200 * KIB, Breaks::Mixed),
        ("cr-at-1m-128k", MIB + 256 * KIB - 1, b"\r", MIB + 300 * KIB, Breaks::Mixed),
    ];
    for (name, at, piece, size, breaks) in placements {
        out.push(Sample::new(format!("place/{name}.txt"), piece_at(rng, at, piece, size, breaks)));
    }
    // The same with a byte order mark before everything.
    let mut bom = UTF8_BOM.to_vec();
    bom.extend(piece_at(rng, MIB - 4, b"\r\n", MIB + 64 * KIB, Breaks::Crlf));
    out.push(Sample::new("place/bom-crlf-at-1m.txt", bom));

    // NUL bytes: binary only among the first 8 KiB.
    for (name, at, size) in [
        ("nul-8191", 8 * KIB - 1, 20 * KIB),
        ("nul-8192", 8 * KIB, 20 * KIB),
        ("nul-0", 0, 100),
        ("nul-100k", 100 * KIB, 150 * KIB),
        ("nul-8191-large", 8 * KIB - 1, 100 * KIB),
        ("nul-8192-large", 8 * KIB, 100 * KIB),
    ] {
        out.push(Sample::new(format!("nul/{name}.txt"), piece_at(rng, at, b"\0", size, Breaks::Lf)));
    }
    let utf16 = encode(&format!("{}\n", mixed_text(rng, 9 * KIB, Breaks::Crlf)), Encoding::Utf16Le, rng);
    out.push(Sample::new("nul/utf16-has-nuls.txt", utf16));

    // Long lines: at the 2000-character limit, around the bytes kept of a cut line (2001 * 4),
    // made of wide characters, and megabytes of minified code.
    let x = |n: usize| "x".repeat(n);
    for (name, line) in [
        ("x-2000", x(2000)),
        ("x-2001", x(2001)),
        ("emoji-2000", "😀".repeat(2000)),
        ("emoji-2001", "😀".repeat(2001)),
        ("emoji-2002", "😀".repeat(2002)),
        ("cjk-2000", "日".repeat(2000)),
        ("cjk-2001", "日".repeat(2001)),
        ("combining-2000", "e\u{301}".repeat(1000)),
        ("combining-2001", format!("{}x", "e\u{301}".repeat(1000))),
        ("bytes-8003", x(8003)),
        ("bytes-8004", x(8004)),
        ("bytes-8005", x(8005)),
        ("emoji-across-8004", format!("{}😀{}", x(8002), x(100))),
        ("zwj-family", "👨\u{200D}👩\u{200D}👧\u{200D}👦".repeat(600)),
        ("tabs-and-spaces", "\t \u{3000}".repeat(1500)),
    ] {
        for (suffix, end) in [("", "\nafter\n"), ("-crlf", "\r\nafter\r\n"), ("-eof", "")] {
            out.push(Sample::new(format!("long/{name}{suffix}.txt"), format!("before\n{line}{end}")));
        }
    }
    let minified = minified(rng, 3 * MIB);
    out.push(Sample::new("long/minified.js", format!("// header\n{minified}\n// footer\n")));
    out.push(Sample::new("long/minified-eof.js", fit_utf8(&minified, MIB + 7)));
    // Invalid bytes in long lines: read counts the characters it does not keep as the bytes
    // that start one.
    let latin1: Vec<u8> = (0..30_000).map(|i| if i % 7 == 0 { 0xA9 } else { b'a' + (i % 26) as u8 }).collect();
    out.push(Sample::new("long/latin1-line.txt", [&b"head\n"[..], &latin1, b"\ntail\n"].concat()));
    let continuation = [x(9000).into_bytes(), vec![0x80; 3000], b"\nend".to_vec()].concat();
    out.push(Sample::new("long/continuation-bytes.txt", continuation));

    // Repeated blocks, for uniqueness.
    out.push(Sample::new("repeat/blocks-lf.rs", repeated_blocks(rng, 40, Breaks::Lf)));
    out.push(Sample::new("repeat/blocks-crlf.rs", repeated_blocks(rng, 40, Breaks::Crlf)));
    out.push(Sample::new("repeat/blocks-mixed.rs", repeated_blocks(rng, 25, Breaks::Mixed)));

    // Source code.
    for (i, lang) in Lang::ALL.into_iter().enumerate() {
        let breaks = [Breaks::Lf, Breaks::Crlf][i % 2];
        let size = rng.random_range(8 * KIB..40 * KIB);
        let name = format!("code/{}-{}.{}", lang.extension(), breaks.name(), lang.extension());
        out.push(Sample::new(name, source(rng, lang, size, breaks)));
    }
    let bom_code = [UTF8_BOM, source(rng, Lang::C, 20 * KIB, Breaks::Crlf).as_bytes()].concat();
    out.push(Sample::new("code/bom-crlf.c", bom_code));

    // UTF-16 read by streaming (over 64 KiB), and a large file of everything.
    let text = mixed_text(rng, 120 * KIB, Breaks::Crlf);
    out.push(Sample::new("enc/utf16le-large.txt", encode(&text, Encoding::Utf16Le, rng)));
    let text = mixed_text(rng, 90 * KIB, Breaks::Mixed);
    out.push(Sample::new("enc/utf16be-large.txt", encode(&text, Encoding::Utf16Be, rng)));
    let text = mixed_text(rng, 3 * MIB, Breaks::Mixed);
    out.push(Sample::new("big/mixed-3m.txt", text));
    let text = mixed_text(rng, 2 * MIB + 333, Breaks::Crlf);
    out.push(Sample::new("big/crlf-2m.txt", encode(&text, Encoding::BrokenUtf8, rng)));
    out
}

/// Line `i` (1-based) of a file made by [`numbered`].
pub fn numbered_line(i: u64) -> String {
    format!("line {i:>9} {}", ["", "αβγ", "日本", "x\ty", "😀", "“q”"][(i % 6) as usize])
}

/// A file of `lines` numbered lines, each ended by `brk`: large files whose every line is known.
pub fn numbered(lines: u64, brk: &str) -> Vec<u8> {
    let mut out = Vec::new();
    for i in 1..=lines {
        out.extend_from_slice(numbered_line(i).as_bytes());
        out.extend_from_slice(brk.as_bytes());
    }
    out
}
