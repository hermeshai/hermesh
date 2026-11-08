# PPTX browser fixtures

- `editor.pptx`: synthetic two-slide deck containing text and rounded rectangles.
- `ole-chart.emf`: vector preview extracted from the user-provided `sample-presentation.pptx` (the 99helpers sample). It contains the Q1–Q3, North/South/East/West chart that disappeared in the browser.
- `ole-preview.pptx`: synthetic deck combining the first fixture with that EMF and its OLE fallback structure. Its embedded OLE payload is an opaque test marker, not executable application data. This checks display and exact preservation of embedded resource bytes.

The user's complete presentation is not copied into the repository. An optional end-to-end run can read it into a temporary working copy with `HERMESH_E2E_PPTX`.
