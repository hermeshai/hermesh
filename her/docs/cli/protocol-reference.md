# her protocol reference

Protocol version 2. Generated from `her protocol schema` by `tools/gen-protocol-docs.mjs`; do not edit by hand.

**stable** methods follow the compatibility rules in [json-rpc.md](json-rpc.md). **experimental** ones may change with any version.

## Stable methods

| Method | Parameters | Result | What it does |
|---|---|---|---|
| `ai/action` | `AiActionParams` | `AiActionResult` | `ai/action`. Answers when the proposal is ready; `ai/action_progress` goes out meanwhile, and `$/cancel_request` stops it. |
| `auth/delete` | `AuthDeleteParams` | `Empty` | Parameters of `auth/delete`. |
| `auth/set` | `AuthSetParams` | `AuthSetResult` | Parameters of `auth/set`. |
| `chatgpt/cancel` | `ChatGptCancelParams` | `ChatGptStatus` |  |
| `chatgpt/complete` | `ChatGptCompleteParams` | `ChatGptStatus` | `chatgpt/complete`: finishes a sign-in started with a `redirect_uri`, with what its redirect carried. It answers once the sign-in has ended. |
| `chatgpt/login` | `ChatGptLoginParams` | `ChatGptStatus` | `chatgpt/login`: starts signing in. Without `redirect_uri`, her catches the browser's redirect on its own machine; with it, the browser goes back to the client's device, and `chatgpt/complete` brings what the redirect carried. |
| `chatgpt/logout` | — | `ChatGptStatus` |  |
| `chatgpt/status` | — | `ChatGptStatus` |  |
| `config/read` | `ConfigReadParams` | `ConfigReadResult` |  |
| `config/write` | `ConfigWriteParams` | `ConfigWriteResult` |  |
| `credentials/delete` | `CredentialsDeleteParams` | `Empty` |  |
| `credentials/set` | `CredentialsSetParams` | `Empty` | A secret for configuration values to refer to as `${credential:<name>}`. |
| `deck/check` | `DeckCheckParams` | `DeckCheckResult` |  |
| `deck/close` | `DeckIdParams` | `Empty` |  |
| `deck/copy` | `DeckCopyParams` | `DeckCopyResult` | `deck/copy`: shapes, slides or text, for `paste` here or in another deck. |
| `deck/diff` | `DeckDiffParams` | `DeckDiff` |  |
| `deck/edit` | `DeckEditParams` | `DeckEditResult` | `deck/edit`, and the `deck_edit` tool without `session_id`: a batch that lands whole or not at all. |
| `deck/find` | `DeckFindParams` | `DeckFindResult` | `deck/find`: text or numbers, case folded unless `case_sensitive`. |
| `deck/focus` | `DeckFocusParams` | `Empty` | `deck/focus`: what the user is looking at, sent as it changes, for the short context block each message carries (§6.6) and for "this slide" in commands. |
| `deck/layouts` | `DeckLayoutsParams` | `DeckLayoutsResult` |  |
| `deck/links` | `DeckLinksParams` | `DeckLinksResult` |  |
| `deck/open` | `DeckOpenParams` | `DeckOpenResult` | `deck/open`. Progress goes out as `pptx/readProgress`. |
| `deck/proposal/accept` | `DeckProposalDecideParams` | `DeckProposalResult` | `deck/proposal/accept`, `deck/proposal/reject`: all of it, or only `slide_ids` (逐页). The rest stays pending as `remaining_id`. |
| `deck/proposal/list` | `DeckProposalListParams` | `DeckProposalListResult` |  |
| `deck/proposal/reject` | `DeckProposalDecideParams` | `DeckProposalResult` | `deck/proposal/accept`, `deck/proposal/reject`: all of it, or only `slide_ids` (逐页). The rest stays pending as `remaining_id`. |
| `deck/proposal/revert` | `DeckProposalIdParams` | `DeckProposalResult` |  |
| `deck/read` | `DeckReadParams` | `DeckReadResult` |  |
| `deck/save` | `DeckSaveParams` | `DeckSaveResult` |  |
| `deck/summary` | `DeckIdParams` | `DeckSummary` |  |
| `deck/sync` | `DeckSyncParams` | `DeckSyncResult` | `deck/sync` from a replica: its local edits, and what it lacks. |
| `deck/template_profile` | `DeckTemplateProfileParams` | `TemplateProfile` | `deck/template_profile`: of an open deck, or of a template file. |
| `deck/undo` | `DeckUndoParams` | `DeckUndoResult` | `deck/undo`: takes back the agent's edit `edit` (the revision its `deck_edit` made), when it is the agent's latest one still in the deck (presentation-deck-edit §5). |
| `deck/view` | `DeckViewParams` | `DeckViewResult` | `deck/view`: a slide rasterized on this machine (§6.5). |
| `files/list` | `FilesListParams` | `FilesListResult` | Parameters of `files/list`. |
| `files/read` | `FilesReadParams` | `FilesReadResult` | Parameters of `files/read`. |
| `files/watch` | `FilesWatchParams` | `Empty` | Parameters of `files/watch`. |
| `files/write` | `FilesWriteParams` | `FilesWriteResult` | Parameters of `files/write`. |
| `initialize` | `InitializeParams` | `InitializeResult` |  |
| `mcp/check` | `McpServerParams` | `McpCheckResult` |  |
| `mcp/list` | `McpListParams` | `McpListResult` |  |
| `mcp/login` | `McpLoginParams` | `McpLoginResult` | `mcp/login`: starts signing in to a server. With `server`, the server is not in the configuration yet: the token is saved under `name`, and adding it comes after. With `redirect_uri`, the browser goes back to the client's device, and `mcp/login_complete` brings what the redirect carried. |
| `mcp/login_cancel` | `McpLogoutParams` | `Empty` |  |
| `mcp/login_complete` | `McpLoginCompleteParams` | `Empty` | `mcp/login_complete`: finishes a sign-in started with a `redirect_uri`, with what its redirect carried. It answers once the token is saved; `mcp/changed { login_id }` follows, as for any sign-in. |
| `mcp/logout` | `McpLogoutParams` | `Empty` |  |
| `mcp/probe` | `McpProbeParams` | `McpProbeResult` | `mcp/probe`: checks a server before it is added, writing nothing (mcp-design §13.6). |
| `mcp/probe_cancel` | `McpProbeCancelParams` | `Empty` |  |
| `mcp/tools` | `McpServerParams` | `McpToolsResult` |  |
| `models/list` | — | `ModelsListResult` |  |
| `models/refresh` | `ModelsRefreshParams` | `ModelsRefreshResult` | Parameters of `models/refresh`. |
| `models/show` | `ModelsShowParams` | `Empty` | Parameters of `models/show`. |
| `resource/read` | `ResourceReadParams` | `ResourceReadResult` |  |
| `server/shutdown` | — | `Empty` | The result of methods that return nothing: `{}`. |
| `session/archive` | `SessionIdParams` | `Empty` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `session/attach` | `SessionAttachParams` | `SnapshotResult` |  |
| `session/close` | `SessionIdParams` | `Empty` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `session/context` | `SessionIdParams` | `ContextReport` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `session/delete` | `SessionIdParams` | `Empty` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `session/detach` | `SessionIdParams` | `Empty` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `session/history` | `SessionHistoryParams` | `TranscriptPage` |  |
| `session/list` | `SessionListParams` | `SessionListResult` |  |
| `session/new` | `SessionNewParams` | `SessionNewResult` |  |
| `session/op` | `SessionOpParams` | `Empty` |  |
| `session/rename` | `SessionRenameParams` | `Empty` | Parameters of `session/rename`. |
| `session/resume` | `SessionResumeParams` | `SnapshotResult` |  |
| `session/unarchive` | `SessionIdParams` | `Empty` | Parameters of `session/detach`, `session/close` and `session/delete`. |
| `sessions/subscribe` | `SessionListParams` | `SessionListResult` |  |
| `sessions/unsubscribe` | — | `Empty` | The result of methods that return nothing: `{}`. |
| `skills/files` | `SkillsFilesParams` | `SkillsFilesResult` |  |
| `skills/install` | `SkillsInstallParams` | `SkillsInstallResult` |  |
| `skills/list` | `SkillsListParams` | `SkillsListResult` | Parameters of `skills/list`: `session_id` or `cwd`, or neither for what a session without a project would find. |
| `skills/read` | `SkillsReadParams` | `SkillsReadResult` |  |
| `skills/remove` | `SkillsRemoveParams` | `Empty` |  |
| `skills/set_enabled` | `SkillsSetEnabledParams` | `SkillsSetEnabledResult` |  |
| `tools/list` | `ToolsListParams` | `ToolsListResult` | Parameters of `tools/list`. |
| `web_search/set_key` | `WebSearchSetKeyParams` | `WebSearchSetKeyResult` |  |
| `web_search/status` | — | `WebSearchStatus` | The result of `web_search/status`. |
| `web_search/test` | `WebSearchTestParams` | `WebSearchTestResult` |  |

## Experimental methods

| Method | Parameters | Result | What it does |
|---|---|---|---|
| `bots/avatar` | `BotIdParams` | `BotAvatarResult` | Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`. |
| `bots/clear` | `BotIdParams` | `Empty` | Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`. |
| `bots/create` | `BotCreateParams` | `BotResult` |  |
| `bots/delete` | `BotIdParams` | `Empty` | Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`. |
| `bots/open` | `BotOpenParams` | `SessionNewResult` | Parameters of `bots/open`, which answers as `session/new` does. |
| `bots/read` | `BotIdParams` | `Empty` | Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`. |
| `bots/routine` | `BotRoutineParams` | `BotRoutineResult` | Parameters of `bots/routine`, `bots/routine_run_now`, `bots/routine_stop` and `bots/routine_delete`. |
| `bots/routine_delete` | `BotRoutineParams` | `Empty` | Parameters of `bots/routine`, `bots/routine_run_now`, `bots/routine_stop` and `bots/routine_delete`. |
| `bots/routine_run` | `BotRoutineRunParams` | `BotRoutineRunResult` |  |
| `bots/routine_run_now` | `BotRoutineParams` | `Empty` | Parameters of `bots/routine`, `bots/routine_run_now`, `bots/routine_stop` and `bots/routine_delete`. |
| `bots/routine_runs` | `BotRoutineRunsParams` | `BotRoutineRunsResult` |  |
| `bots/routine_set_enabled` | `BotRoutineSetEnabledParams` | `Empty` |  |
| `bots/routine_stop` | `BotRoutineParams` | `Empty` | Parameters of `bots/routine`, `bots/routine_run_now`, `bots/routine_stop` and `bots/routine_delete`. |
| `bots/subscribe` | — | `BotsResult` | Result of `bots/subscribe`. |
| `bots/unread` | `BotIdParams` | `Empty` | Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`. |
| `bots/unsubscribe` | — | `Empty` | The result of methods that return nothing: `{}`. |
| `bots/update` | `BotUpdateParams` | `BotResult` |  |
| `browser/allow` | `BrowserSessionParams` | `Empty` | Parameters of `browser/allow` and `browser/cancel_wait`. |
| `browser/assign_tab` | `BrowserAssignTabParams` | `Empty` | Parameters of `browser/assign_tab`. |
| `browser/cancel_wait` | `BrowserSessionParams` | `Empty` | Parameters of `browser/allow` and `browser/cancel_wait`. |
| `browser/close_tabs` | `BrowserCloseTabsParams` | `Empty` | Parameters of `browser/close_tabs`. |
| `browser/reveal` | `BrowserRevealParams` | `Empty` | Parameters of `browser/reveal`. |
| `browser/site_icon` | `BrowserSiteIconParams` | `BrowserSiteIconResult` | Parameters of `browser/site_icon`. |
| `browser/status` | — | `BrowserStatus` | The result of `browser/status`, and the parameters of `browser/changed`. |
| `browser/use_extension` | `BrowserUseExtensionParams` | `Empty` | Parameters of `browser/use_extension`. |
| `cards/answer` | `CardsAnswerParams` | `CardsAnswerResult` | Parameters of `cards/answer`, which only the owner's devices may call. |
| `connections/connect` | `ConnectionConnectParams` | `ConnectionsSnapshot` |  |
| `connections/disconnect` | `ConnectionIdParams` | `ConnectionsSnapshot` |  |
| `connections/discover` | — | `ConnectionsDiscoveryResult` |  |
| `connections/list` | — | `ConnectionsSnapshot` |  |
| `connections/models` | `ConnectionIdParams` | `ConnectionsSnapshot` |  |
| `connections/remove` | `ConnectionIdParams` | `ConnectionsSnapshot` |  |
| `connections/retry` | `ConnectionIdParams` | `ConnectionsSnapshot` |  |
| `connectors/install` | `ConnectorInstallParams` | `ConnectorInstallResult` | Install a curated connector. No arbitrary download URLs or commands are accepted. |
| `desktop/status` | — | `DesktopStatus` | The result of `desktop/status`: whether computer use can work where her runs. |
| `doc/apply` | `DocApplyParams` | `DocApplyResult` |  |
| `doc/autofill` | `DocAutofillParams` | `DocApplyResult` | `doc/autofill`: Excel's fill handle dragged from `source` to `target` (the source with the rows or columns the drag added on one side), as one edit. Numbers, dates, text ending in a number and the built-in lists (周一, 一月, Jan…) carry on as a series; anything else, and formulas moved as Excel moves them, repeats. |
| `doc/cells` | `DocCellsParams` | `DocCellsResult` | A range of a sheet, as the grid shows it (spreadsheet-design §7 `doc/cells`). |
| `doc/close` | `DocIdParams` | `Empty` |  |
| `doc/fill` | `DocFillParams` | `DocApplyResult` | `doc/fill`: one edit that copies the range's first row (or column) across it, formulas moved as Excel moves them (relative references shift, `$` ones stay). |
| `doc/find` | `DocFindParams` | `DocFoundResult` | `doc/find`: cells whose shown text or formula contains `query`, case folded. |
| `doc/locate` | `DocLocateParams` | `DocLocateResult` | A citation's target in an open document: `p12.b3` (a block) or `p12` (a page). |
| `doc/open` | `DocOpenParams` | `DocOpenResult` |  |
| `doc/recalc` | `DocRecalcParams` | `DocApplyResult` |  |
| `doc/redo` | `DocIdParams` | `DocApplyResult` |  |
| `doc/resolve` | `DocResolveParams` | `DocResolveResult` | A selection or a region on a page, to be read by the kernel (§9.4.1). |
| `doc/save` | `DocSaveParams` | `DocInfo` |  |
| `doc/sort` | `DocSortParams` | `DocApplyResult` | `doc/sort`: the rows of `range` sorted by the values of one of its columns, as one edit; as Excel sorts, numbers come before text before TRUE/FALSE before errors, blanks last either way, ties keep their order, and a formula moves with its row (relative references shift). Formats stay where they are. |
| `doc/structure` | `DocStructureParams` | `DocApplyResult` | `doc/structure`: inserts or deletes whole rows or columns as one edit. Every reference to the cells that move moves with them, in every sheet, as Excel moves it; one into deleted cells becomes `#REF!`; comments move with their cells. Refused where it cannot be done faithfully here (Excel tables, pivot tables, form controls…): the reason says so. |
| `doc/trace` | `DocTraceParams` | `DocFoundResult` | `doc/trace`: Excel's 追踪引用单元格 / 追踪从属单元格, `depth` steps out (1 to 10). |
| `doc/undo` | `DocIdParams` | `DocApplyResult` |  |
| `doc/view` | `DocViewParams` | `Empty` | What a session's document column shows: the page, and what is selected. |
| `docx/close` | `DocxIdParams` | `Empty` |  |
| `docx/open` | `DocxOpenParams` | `DocxOpenResult` |  |
| `docx/review` | `DocxReviewParams` | `DocxRevisionResult` | Accepts or rejects tracked changes, the person's choice in the document (word-design §5.2). |
| `docx/save` | `DocxSaveParams` | `DocxSaveResult` |  |
| `docx/sync` | `DocxSyncParams` | `DocxSyncResult` | A replica's update (merged and sent to the other replicas), and/or its state vector (the result then carries what it lacks). |
| `docx/undo` | `DocxUndoParams` | `DocxRevisionResult` | Takes back the agent's edit `edit` (the revision it made), when it is the agent's latest step still in the document. |
| `pptx/metafile` | `PptxMetafileParams` | `PptxMetafileResult` |  |
| `pptx/read` | `PptxReadParams` | `PptxReadResult` | Optional correlation ID keeps concurrent opens and reopened tabs isolated. |
| `pptx/write` | `PptxWriteParams` | `FilesWriteResult` |  |

## Client notifications

| Notification | Parameters | Tier |
|---|---|---|
| `$/cancel_request` | `CancelRequestParams` | stable |

## Server notifications

| Notification | Parameters | Tier |
|---|---|---|
| `ai/action_progress` | `AiActionProgress` | stable |
| `bots/changed` | `BotsChangedParams` | experimental |
| `browser/changed` | `BrowserStatus` | experimental |
| `config/changed` | `ConfigChangedParams` | stable |
| `deck/changed` | `DeckChangedParams` | stable |
| `deck/proposal` | `DeckProposalEvent` | stable |
| `deck/sync` | `DeckSyncNotice` | stable |
| `doc/changed` | `DocChangedParams` | experimental |
| `docx/changed` | `DocxChangedParams` | experimental |
| `docx/sync` | `DocxSyncNotice` | experimental |
| `files/changed` | `FilesChangedParams` | stable |
| `mcp/changed` | `McpChangedParams` | stable |
| `mcp/probe_progress` | `McpProbeProgressParams` | stable |
| `models/changed` | `Empty` | stable |
| `pptx/readProgress` | `PptxReadProgress` | experimental |
| `session/detached` | `SessionDetachedParams` | stable |
| `session/event` | `SessionEventParams` | stable |
| `sessions/changed` | `SessionsChangedParams` | stable |
| `skills/changed` | `SkillsChangedParams` | stable |

## Error codes

| Code | `data.kind` |
|---|---|
| -32002 | `session_not_found` |
| -32010 | `not_initialized` |
| -32011 | `session_busy` |
| -32012 | `session_not_loaded` |
| -32013 | `not_attached` |
| -32014 | `prompt_in_progress` |
| -32015 | `approval_not_pending` |
| -32016 | `server_full` |
| -32017 | `overloaded` |
| -32018 | `unsupported_content` |
| -32019 | `session_closed` |
| -32020 | `task_failed` |
| -32021 | `key_rejected` |
| -32022 | `model_unavailable` |
| -32023 | `config_version_conflict` |
| -32024 | `config_layer_readonly` |
| -32025 | `config_validation_error` |
| -32026 | `config_busy` |
| -32027 | `skill_invalid` |
| -32028 | `skill_conflict` |
| -32029 | `not_removable` |
| -32030 | `credential_store_unavailable` |
| -32031 | `mcp_login_failed` |
| -32032 | `doc_unavailable` |
| -32033 | `doc_password` |
| -32034 | `doc_refused` |
| -32035 | `doc_stale` |
| -32036 | `doc_conflict` |
| -32037 | `deck_unavailable` |
| -32038 | `deck_conflict` |
| -32039 | `proposal_not_found` |
| -32040 | `bot_not_found` |
| -32041 | `bot_default` |
| -32042 | `bot_invalid` |
| -32043 | `routine_not_found` |
| -32044 | `routine_busy` |
| -32045 | `routines_unavailable` |
| -32046 | `no_model` |
| -32047 | `card_ended` |
| -32600 | `invalid_request` |
| -32601 | `method_not_found` |
| -32602 | `invalid_params` |
| -32603 | `internal_error` |
| -32700 | `parse_error` |
| -32800 | `request_cancelled` |
