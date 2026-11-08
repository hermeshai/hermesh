//! A1 cell addresses.

use std::fmt;

pub const MAX_ROW: u32 = 1_048_576;
pub const MAX_COL: u32 = 16_384;

/// A cell address, 1-based. Orders row first, then column: the order of
/// cells in a sheet's XML.
#[derive(Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub struct CellRef {
    pub row: u32,
    pub col: u32,
}

impl CellRef {
    pub fn new(row: u32, col: u32) -> Self {
        Self { row, col }
    }

    /// Parses `B12` (no `$`, no sheet).
    pub fn parse(s: &str) -> Option<Self> {
        let s = s.trim();
        let split = s.find(|c: char| c.is_ascii_digit())?;
        let (letters, digits) = s.split_at(split);
        let col = parse_col(letters)?;
        if !digits.bytes().all(|b| b.is_ascii_digit()) || digits.starts_with('0') {
            return None;
        }
        let row: u32 = digits.parse().ok()?;
        (1..=MAX_ROW).contains(&row).then_some(Self { row, col })
    }
}

impl fmt::Display for CellRef {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}{}", col_name(self.col), self.row)
    }
}

pub fn parse_col(letters: &str) -> Option<u32> {
    if letters.is_empty() || letters.len() > 3 {
        return None;
    }
    let mut n: u32 = 0;
    for b in letters.bytes() {
        if !b.is_ascii_alphabetic() {
            return None;
        }
        n = n * 26 + (b.to_ascii_uppercase() - b'A' + 1) as u32;
    }
    (1..=MAX_COL).contains(&n).then_some(n)
}

pub fn col_name(mut col: u32) -> String {
    let mut out = Vec::new();
    while col > 0 {
        let rem = (col - 1) % 26;
        out.push(b'A' + rem as u8);
        col = (col - 1) / 26;
    }
    out.reverse();
    String::from_utf8(out).unwrap()
}

/// A rectangular range, inclusive.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Rect {
    pub first: CellRef,
    pub last: CellRef,
}

impl Rect {
    pub fn single(c: CellRef) -> Self {
        Self { first: c, last: c }
    }

    /// Parses `A1:C10` or `A1`.
    pub fn parse(s: &str) -> Option<Self> {
        let s = s.trim();
        match s.split_once(':') {
            Some((a, b)) => {
                let (a, b) = (CellRef::parse(a)?, CellRef::parse(b)?);
                Some(Self {
                    first: CellRef::new(a.row.min(b.row), a.col.min(b.col)),
                    last: CellRef::new(a.row.max(b.row), a.col.max(b.col)),
                })
            }
            None => CellRef::parse(s).map(Self::single),
        }
    }

    pub fn contains(&self, c: CellRef) -> bool {
        c.row >= self.first.row
            && c.row <= self.last.row
            && c.col >= self.first.col
            && c.col <= self.last.col
    }

    /// Whether the two share a cell.
    pub fn intersects(&self, o: &Rect) -> bool {
        self.first.row <= o.last.row && self.last.row >= o.first.row && self.first.col <= o.last.col && self.last.col >= o.first.col
    }

    pub fn union(&self, c: CellRef) -> Self {
        Self {
            first: CellRef::new(self.first.row.min(c.row), self.first.col.min(c.col)),
            last: CellRef::new(self.last.row.max(c.row), self.last.col.max(c.col)),
        }
    }
}

impl fmt::Display for Rect {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.first == self.last {
            write!(f, "{}", self.first)
        } else {
            write!(f, "{}:{}", self.first, self.last)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        for s in ["A1", "Z9", "AA10", "XFD1048576", "BC23"] {
            assert_eq!(CellRef::parse(s).unwrap().to_string(), s);
        }
        assert!(CellRef::parse("XFE1").is_none());
        assert!(CellRef::parse("A0").is_none());
        assert!(CellRef::parse("A01").is_none());
        assert!(CellRef::parse("A1048577").is_none());
        assert_eq!(Rect::parse("C3:A1").unwrap().to_string(), "A1:C3");
    }
}
