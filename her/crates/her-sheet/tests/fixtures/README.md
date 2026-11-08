# Fixtures

- `*.xlsx`, `*.xlsm`: calamine's test files, saved by Excel and other writers (MIT, see
  `CALAMINE-LICENSE-MIT.md`). They cover shared formulas, tables, pivots, charts, macros, rich
  values, strict OOXML, missing `r` attributes, byte-order marks, backslash part names.
- `legacy.xls`: calamine’s `tests/issues.xls` (same MIT license), used only for legacy read-only coverage.
- `gen/dcf_model.xlsx`, `gen/funcs.xlsx`: made by `tools/make_corpus.py` and `tools/make_funcs.py`
  (xlsxwriter): a Chinese DCF model, and the functions finance models use with reference values.
- `gen/dcf_settled.xlsx`: `dcf_model.xlsx` with every formula's result written in by
  `cargo run -p her-sheet --example settle`, as Excel would have saved it (the end-to-end tests
  need a model whose saved values are right).
- `gen/oo_*.xlsx`: the same kind of files saved again by ONLYOFFICE, for writers other than Excel.

Real models whose licence is unclear stay out of the repository: set `HER_SHEET_CORPUS` to a
directory of them and the fuzz test includes them.
