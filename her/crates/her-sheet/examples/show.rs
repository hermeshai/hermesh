//! Prints a range as the grid would show it: `cargo run -p her-sheet --example show -- <file> <sheet> <range>`.
#![allow(clippy::print_stdout)]

use her_sheet::book::Book;
use her_sheet::cellref::CellRef;
use her_sheet::cellref::Rect;

fn main() {
    let a: Vec<String> = std::env::args().collect();
    let book = Book::open(&std::fs::read(&a[1]).unwrap()).unwrap();
    let s = book.sheet_index(&a[2]).unwrap();
    let r = Rect::parse(&a[3]).unwrap();
    for row in r.first.row..=r.last.row {
        let cells: Vec<String> = (r.first.col..=r.last.col)
            .map(|col| {
                let at = CellRef::new(row, col);
                let style = book.style(s, at);
                book.display(&book.value(s, at), &style).0
            })
            .collect();
        println!("{row:>4} | {}", cells.join(" | "));
    }
}
