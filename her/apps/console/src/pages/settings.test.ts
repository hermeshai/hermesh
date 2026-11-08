import assert from "node:assert/strict";
import { test } from "node:test";
import { siteKey, siteOrigin, sitesOf } from "./settings.ts";

// What the Browser page makes of her's answers.

test("sites: an origin as typed, as her keeps it, and the key path that names it", () => {
  assert.equal(siteOrigin("example.com"), "https://example.com");
  assert.equal(siteOrigin(" GitHub.com/her/issues?q=1 "), "https://github.com");
  assert.equal(siteOrigin("http://192.168.1.10:8080/admin"), "http://192.168.1.10:8080");
  assert.equal(siteOrigin("https://example.com:443"), "https://example.com");
  assert.equal(siteOrigin("ftp://example.com"), null);
  assert.equal(siteOrigin("https://user:pw@example.com"), null);
  assert.equal(siteOrigin("not a site"), null);
  assert.equal(siteOrigin(""), null);
  assert.equal(siteKey("https://example.com"), 'browser.sites."https://example.com"');
});

test("sites from config/read: all sites first, then by origin; anything not a level left out", () => {
  const config = { browser: { sites: { "https://b.example": "navigate", "*": "act", "https://a.example": "deny", odd: 3 } } };
  assert.deepEqual(sitesOf(config), [
    ["*", "act"],
    ["https://a.example", "deny"],
    ["https://b.example", "navigate"],
  ]);
  assert.deepEqual(sitesOf({}), []);
  assert.deepEqual(sitesOf(null), []);
});
