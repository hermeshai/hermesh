import assert from "node:assert/strict";
import { test } from "node:test";
import { attachmentSnapshot, attachmentUrl, isImageAttachment, keepAttachmentBlob, loadAttachmentBlob, mimeForAttachment } from "./attachments.ts";

test("image attachments are recognized by mime type or filename extension", () => {
  assert.equal(isImageAttachment({ name: "shot.png", mimeType: "image/png" }), true);
  assert.equal(isImageAttachment({ name: "diagram.svg", mimeType: "application/octet-stream" }), true);
  assert.equal(isImageAttachment({ name: "notes.md", mimeType: "text/markdown" }), false);
  assert.equal(mimeForAttachment({ name: "diagram.svg", mimeType: "application/octet-stream" }), "image/svg+xml");
});

test("kept attachment blobs show immediately and missing ones load once by sha256", async () => {
  const sha1 = "1".repeat(64);
  keepAttachmentBlob(sha1, new Uint8Array([137, 80, 78, 71]), "image/png", "pasted.png");
  const url1 = attachmentUrl({ blobSha256: sha1, name: "pasted.png", mimeType: "image/png" });
  assert.ok(url1?.startsWith("blob:"));

  let calls = 0;
  const sha2 = "2".repeat(64);
  const att2 = { blobSha256: sha2, name: "remote.jpg", mimeType: "image/jpeg" };
  await Promise.all([
    loadAttachmentBlob(att2, async () => {
      calls++;
      return { data: new Uint8Array([255, 216, 255]), mimeType: "image/jpeg" };
    }),
    loadAttachmentBlob(att2, async () => {
      calls++;
      return { data: new Uint8Array([255, 216, 255]), mimeType: "image/jpeg" };
    }),
  ]);
  assert.equal(calls, 1);
  assert.ok(attachmentSnapshot()[sha2]?.startsWith("blob:"));

  const sha3 = "3".repeat(64);
  await loadAttachmentBlob({ blobSha256: sha3, name: "missing.png", mimeType: "image/png" }, async () => {
    throw new Error("not found");
  });
  assert.equal(attachmentSnapshot()[sha3], null);
});
