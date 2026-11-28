<handoff_request>
The context window has reached its working-set limit. Replace the conversation so far with a
compact, high-signal handoff summary for this assistant conversation. After this checkpoint,
you will have only `<memory>`, the user's most recent messages, and your summary.

Pause the task and write the handoff summary in plain Markdown (do not call tools), organized
strictly into these four sections:

1. **In-Flight Work (Exact IDs)**:
   - Every coding session (`session_id`, project, branch, agent, current state, and any pending
     question) or background sub-agent (`name` and task) that is STILL running or awaiting follow-up.
   - Copy identifiers (`s_…`, branch names, file paths) character-for-character. If none, write `None`.

2. **Open Thread & Active Constraints**:
   - Any unfinished topic, pending draft, or explicit output/formatting constraint the user set
     for upcoming replies.

3. **Key Facts, Timeline & Assistant Deliverables (Latest Wins)**:
   - Preserve concrete user facts, numbers, counts, named entities, and exact dates (`[YYYY-MM-DD]`
     or `[Session time: …]`) from both this conversation and any earlier `<conversation_summary>`
     when not already in `<memory>`.
   - **Structured deliverables**: Whenever the assistant provided a numbered list of objectives, aims,
     steps, or multi-item recommendations, keep each numbered item's exact core terms in order — never
     merge an explicit `Objectives` / `Recommendations` list with adjacent `Methods` or `Background` prose.
   - **Updates & corrections**: When a later message changed an earlier fact, count, schedule, location,
     or preference, record the latest state clearly and note the old one was superseded.

4. **Recently Completed Work (At Most 5 Items, 1 Line Each)**:
   - At most one line each (`session_id` / branch / key files / diff counts / 1-phrase outcome) for
     recently finished tasks in case the user refers back to them.
   - **Drop all noise**: Omit raw tool outputs, JSON listings, routine runs with nothing to report,
     step-by-step debugging logs, and settled chitchat.
</handoff_request>
