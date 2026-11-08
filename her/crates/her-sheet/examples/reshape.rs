//! Inserts rows into a sheet and writes the result: `cargo run -p her-sheet --example reshape -- <in> <sheet> <at> <count> <out>`.
use her_sheet::structure::{shift, Axis, Shift};
fn main() {
    let a: Vec<String> = std::env::args().collect();
    let out = shift(&std::fs::read(&a[1]).unwrap(), &a[2], Shift { axis: Axis::Rows, at: a[3].parse().unwrap(), count: a[4].parse().unwrap(), delete: false }).unwrap();
    std::fs::write(&a[5], out).unwrap();
}
