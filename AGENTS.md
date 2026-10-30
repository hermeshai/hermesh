### 1. Think before coding

**Don't assume. Don't hide confusion. Surface the trade-offs.**

LLMs often pick one interpretation silently and run with it. This principle forces explicit reasoning:

- **State assumptions** — if unsure, ask instead of guessing
- **Present the interpretations** — when something is ambiguous, don't choose silently
- **Push back when warranted** — if a simpler approach exists, say so
- **Stop when confused** — point out what is unclear and ask for clarification

### 2. Simplicity first

**Solve the problem with the least code. Don't speculate.**

Fight the urge to over-engineer:

- Don't add features beyond what was asked
- Don't build abstractions for one-off code
- Don't add "flexibility" or "configurability" nobody asked for
- Don't handle errors for scenarios that cannot happen
- If 200 lines could be 50, rewrite it

**The test:** would a senior engineer find this overcomplicated? If so, simplify.

### 3. Precise changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:

- Don't "improve" adjacent code, comments or formatting
- Don't refactor what isn't broken
- Match the existing style, even if you would write it differently
- If you notice unrelated dead code, mention it — don't delete it

When your change leaves orphans:

- Remove imports, variables and functions your change made unused
- Don't remove pre-existing dead code unless asked

**The test:** every changed line traces directly back to the user's request.

### 4. Goal-driven execution

**Define success criteria. Loop until they are met.**

Turn imperative tasks into verifiable goals:

| Instead of... | Turn it into... |
|---------------|-----------------|
| "Add validation" | "Write tests for invalid input, then make them pass" |
| "Fix the bug" | "Write a test that reproduces the bug, then make it pass" |
| "Refactor X" | "Make sure the tests pass before and after the refactor" |

For multi-step tasks, state a short plan:

```
1. [step] → verify: [check]
2. [step] → verify: [check]
3. [step] → verify: [check]
```

Strong success criteria let an LLM loop on its own. Weak ones ("make it work") need constant clarification.
Unit tests alone are not enough: on top of them, every change to a feature or an interface needs an end-to-end test, built as close as possible to how real users use the program, to check that it is reliable.
