//! Charts drawn on a worksheet are read with the numbers behind them.

use her_sheet::book::Book;
use her_sheet::chart::Grouping;
use her_sheet::chart::Kind;

#[test]
fn a_bar_chart_on_a_worksheet_is_read_with_its_numbers() {
    let book = Book::open(include_bytes!("fixtures/embedded_bar_chart.xlsx")).unwrap();
    let ix = book.sheet_index("对比图").unwrap();
    let charts = &book.sheets[ix].charts;
    assert_eq!(charts.len(), 1);
    let c = &charts[0];
    assert_eq!(c.kind, Kind::Bar);
    assert!(c.horizontal);
    assert_eq!(c.grouping, Grouping::Clustered);
    assert_eq!(c.title.as_deref(), Some("Hy4 正式版 vs 预览版（百分制）"));
    assert_eq!(c.legend.as_deref(), Some("b"));
    assert!(c.labels);
    // Anchored at B17, 8640000 × 3960000 EMU.
    assert_eq!((c.from.col, c.from.row), (2, 17));
    assert!((c.width - 907.09).abs() < 0.1 && (c.height - 415.75).abs() < 0.1);
    assert_eq!(c.categories.len(), 10);
    assert_eq!(c.categories[0], "ProgramBench");
    assert_eq!(c.series.len(), 2);
    assert_eq!(c.series[0].name.as_deref(), Some("Hy4 preview"));
    assert_eq!(c.series[0].color.as_deref(), Some("A9BED3"));
    assert_eq!(c.series[0].values[0], Some(17.5));
    assert_eq!(c.series[1].values[0], Some(30.0));
}

#[test]
fn other_sheets_have_no_charts() {
    let book = Book::open(include_bytes!("fixtures/embedded_bar_chart.xlsx")).unwrap();
    assert_eq!(book.sheets.iter().map(|s| s.charts.len()).sum::<usize>(), 1);
}
