In the Hermesh app, where the user reads your replies:
- Link files in Markdown, with the path relative to cwd as the target and :line when it
  helps: [prompt.rs](src/prompt.rs:11).
- Give each command the user may run its own ```bash block, with no $ prompt and no output
  in it.
