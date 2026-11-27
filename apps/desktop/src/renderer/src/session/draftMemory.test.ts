import assert from "node:assert/strict";
import { test } from "node:test";
import {
  EMPTY_DRAFT,
  NEW_SESSION_DRAFT,
  createDraftManager,
  sessionDraftKey,
  type DraftAttachment,
  type DraftStore,
} from "./draftMemory.ts";

function memStore(): DraftStore & { dump(): string | null } {
  const map = new Map<string, string>();
  return {
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => void map.set(k, v),
    removeItem: (k) => void map.delete(k),
    dump: () => map.get("session-drafts") ?? null,
  };
}

const att = (id: string, bytes = 1024n): DraftAttachment => ({
  blobSha256: id.repeat(64).slice(0, 64),
  name: `file-${id}.png`,
  mimeType: "image/png",
  sizeBytes: bytes,
});

test("isolates new-session and per-session drafts and persists across restarts", () => {
  const backing = memStore();
  const m1 = createDraftManager(backing);

  assert.equal(m1.read(NEW_SESSION_DRAFT), EMPTY_DRAFT);
  assert.equal(m1.read(sessionDraftKey("s1")), EMPTY_DRAFT);

  m1.setText(NEW_SESSION_DRAFT, "  new session idea\nsecond line ");
  m1.setText(sessionDraftKey("s1"), "reply to s1");
  m1.addAttachment(sessionDraftKey("s1"), att("1", 4096n));
  m1.setText(sessionDraftKey("s2"), "reply to s2");

  assert.equal(m1.read(NEW_SESSION_DRAFT).text, "  new session idea\nsecond line ");
  assert.equal(m1.read(sessionDraftKey("s1")).text, "reply to s1");
  assert.equal(m1.read(sessionDraftKey("s1")).attachments.length, 1);
  assert.equal(m1.read(sessionDraftKey("s2")).text, "reply to s2");

  // Simulate app restart over the same localStorage.
  const m2 = createDraftManager(backing);
  assert.equal(m2.read(NEW_SESSION_DRAFT).text, "  new session idea\nsecond line ");
  assert.equal(m2.read(sessionDraftKey("s1")).text, "reply to s1");
  assert.equal(m2.read(sessionDraftKey("s1")).attachments[0]?.sizeBytes, 4096n);
  assert.equal(m2.read(sessionDraftKey("s2")).text, "reply to s2");
});

test("keeps live whitespace while typing without persisting blank entries, and preserves attachment-only drafts", () => {
  const backing = memStore();
  const m = createDraftManager(backing);

  // Typing leading space stays live in the current session, but does not write blank junk to storage.
  m.setText(sessionDraftKey("s1"), "   ");
  assert.equal(m.read(sessionDraftKey("s1")).text, "   ");
  assert.equal(backing.dump(), null);

  // Restart sees clean empty draft for whitespace-only input.
  assert.equal(createDraftManager(backing).read(sessionDraftKey("s1")), EMPTY_DRAFT);

  // Attachment-only draft (no text) IS persisted across restarts and deduplicates identical sha256.
  const pic = att("a", 512n);
  m.clear(sessionDraftKey("s1"));
  m.addAttachment(sessionDraftKey("s1"), pic);
  m.addAttachment(sessionDraftKey("s1"), pic);
  assert.equal(m.read(sessionDraftKey("s1")).attachments.length, 1);

  const restarted = createDraftManager(backing);
  assert.equal(restarted.read(sessionDraftKey("s1")).text, "");
  assert.equal(restarted.read(sessionDraftKey("s1")).attachments.length, 1);

  restarted.removeAttachment(sessionDraftKey("s1"), pic.blobSha256);
  assert.equal(restarted.read(sessionDraftKey("s1")), EMPTY_DRAFT);
  assert.equal(backing.dump(), null);
});

test("evicts least-recently-updated drafts beyond maxDrafts", () => {
  const backing = memStore();
  const m = createDraftManager(backing, 3);

  m.setText(sessionDraftKey("s1"), "one");
  m.setText(sessionDraftKey("s2"), "two");
  m.setText(sessionDraftKey("s3"), "three");
  // Touch s1 so s2 becomes the oldest.
  m.setText(sessionDraftKey("s1"), "one updated");
  m.setText(sessionDraftKey("s4"), "four");

  assert.equal(m.read(sessionDraftKey("s2")), EMPTY_DRAFT);
  assert.equal(m.read(sessionDraftKey("s1")).text, "one updated");
  assert.equal(m.read(sessionDraftKey("s3")).text, "three");
  assert.equal(m.read(sessionDraftKey("s4")).text, "four");
});

test("restores failed send to its own session without polluting other sessions or losing newer typing", () => {
  const backing = memStore();
  const m = createDraftManager(backing);

  m.setText(sessionDraftKey("s2"), "working in s2");

  // Case 1: s1 is still empty when send fails -> restores directly to s1, leaving s2 untouched.
  m.restore(sessionDraftKey("s1"), { text: "failed prompt", attachments: [att("1")] });
  assert.equal(m.read(sessionDraftKey("s1")).text, "failed prompt");
  assert.equal(m.read(sessionDraftKey("s1")).attachments.length, 1);
  assert.equal(m.read(sessionDraftKey("s2")).text, "working in s2");

  // Case 2: user already typed follow-up in s1 before the earlier send failed -> merges both cleanly.
  m.save(sessionDraftKey("s1"), { text: "new follow-up", attachments: [att("1"), att("2")] });
  m.restore(sessionDraftKey("s1"), { text: "earlier failed", attachments: [att("1")] });
  assert.equal(m.read(sessionDraftKey("s1")).text, "earlier failed\nnew follow-up");
  assert.equal(m.read(sessionDraftKey("s1")).attachments.length, 2);
});

test("moves in-flight starting draft to the created session and forgets deleted sessions", () => {
  const backing = memStore();
  const m = createDraftManager(backing);

  m.setText("starting:cmd-1", "follow-up typed while starting");
  m.move("starting:cmd-1", sessionDraftKey("s-created"));

  assert.equal(m.read("starting:cmd-1"), EMPTY_DRAFT);
  assert.equal(m.read(NEW_SESSION_DRAFT), EMPTY_DRAFT);
  assert.equal(m.read(sessionDraftKey("s-created")).text, "follow-up typed while starting");

  m.forgetSession("s-created");
  assert.equal(m.read(sessionDraftKey("s-created")), EMPTY_DRAFT);
  assert.equal(backing.dump(), null);
});
