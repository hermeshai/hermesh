// Generated from crates/her-browser/src/methods.rs; do not edit. Regenerate with
// HER_UPDATE_SNAPSHOTS=1 cargo test -p her-browser methods.

/** The version of what her needs from the extension: BRIDGE in methods.rs (D-219). */
export const bridge = 2;

/** Browser-level commands, which the browser end answers itself. */
export const browserMethods = Object.freeze([
  "Browser.close",
  "Browser.setDownloadBehavior",
  "Target.activateTarget",
  "Target.attachToTarget",
  "Target.closeTarget",
  "Target.createTarget",
  "Target.detachFromTarget",
  "Target.getTargetInfo",
  "Target.getTargets",
  "Target.setDiscoverTargets",
]);

/** Page-level commands, forwarded to a page's debugger; any other is refused (D-192). */
export const pageMethods = Object.freeze([
  "Accessibility.getFullAXTree",
  "DOM.describeNode",
  "DOM.focus",
  "DOM.getBoxModel",
  "DOM.getContentQuads",
  "DOM.getFrameOwner",
  "DOM.getNodeForLocation",
  "DOM.resolveNode",
  "DOM.scrollIntoViewIfNeeded",
  "DOM.setFileInputFiles",
  "DOMSnapshot.captureSnapshot",
  "Fetch.continueRequest",
  "Fetch.enable",
  "Fetch.failRequest",
  "Input.dispatchKeyEvent",
  "Input.dispatchMouseEvent",
  "Input.insertText",
  "Log.enable",
  "Network.enable",
  "Page.captureScreenshot",
  "Page.createIsolatedWorld",
  "Page.enable",
  "Page.getFrameTree",
  "Page.getLayoutMetrics",
  "Page.getNavigationHistory",
  "Page.handleJavaScriptDialog",
  "Page.navigate",
  "Page.navigateToHistoryEntry",
  "Page.reload",
  "Page.setInterceptFileChooserDialog",
  "Page.stopLoading",
  "Runtime.callFunctionOn",
  "Runtime.enable",
  "Runtime.evaluate",
  "Runtime.getProperties",
  "Runtime.releaseObject",
  "Runtime.runIfWaitingForDebugger",
  "Target.setAutoAttach",
]);
