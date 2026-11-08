<handoff_request>
The context window is nearly full. The conversation so far will be replaced by a handoff
summary that you write now. After that, the work continues from your summary, the user's
recent messages and the current state of the workspace. Nothing else from this conversation
will be available, including tool results.

Pause the task and write the handoff summary. Cover:
1. Goal: what the user asked for, with every constraint, preference and decision they stated.
2. Done so far: files created or changed, commands run and what they showed, decisions made
   and why.
3. Current state: what works, what fails, open errors, and what was in progress when you
   stopped, including sub-agents that have not reported yet (name and task).
4. Next steps: what remains, in order.
5. Exact details: file paths, names, identifiers, commands, error messages and values the
   work depends on, including paths of saved tool output. Copy them exactly.

If the conversation already contains an earlier handoff summary, carry over what still
matters from it, because it will be replaced too. Do not call tools. Reply with the summary
only, in plain Markdown.
</handoff_request>
