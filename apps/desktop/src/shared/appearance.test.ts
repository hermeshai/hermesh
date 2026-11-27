import assert from "node:assert/strict";
import { test } from "node:test";
import {
  isAppearance,
  isDarkPalette,
  isLightPalette,
  resolvePalette,
  themeSourceOf,
  usesSidebarMaterial,
  windowBackgroundOf,
} from "./appearance.ts";

test("appearance guards and themeSource mapping cover all 5 modes", () => {
  for (const a of ["system", "light", "soft", "dark", "black"]) assert.equal(isAppearance(a), true);
  assert.equal(isAppearance("gray"), false);
  assert.equal(isAppearance(null), false);

  assert.equal(isLightPalette("light"), true);
  assert.equal(isLightPalette("soft"), true);
  assert.equal(isLightPalette("dark"), false);

  assert.equal(isDarkPalette("dark"), true);
  assert.equal(isDarkPalette("black"), true);
  assert.equal(isDarkPalette("soft"), false);

  assert.equal(themeSourceOf("system"), "system");
  assert.equal(themeSourceOf("light"), "light");
  assert.equal(themeSourceOf("soft"), "light");
  assert.equal(themeSourceOf("dark"), "dark");
  assert.equal(themeSourceOf("black"), "dark");
});

test("resolvePalette honors explicit modes and system day/night preferences", () => {
  const base = { appearance: "system" as const };
  assert.equal(resolvePalette(base, false), "light");
  assert.equal(resolvePalette(base, true), "dark");

  const customPair = { appearance: "system" as const, preferredLight: "soft" as const, preferredDark: "black" as const };
  assert.equal(resolvePalette(customPair, false), "soft");
  assert.equal(resolvePalette(customPair, true), "black");

  assert.equal(resolvePalette({ appearance: "soft", preferredLight: "light" }, true), "soft");
  assert.equal(resolvePalette({ appearance: "black", preferredDark: "dark" }, false), "black");
});

test("sidebar material and initial window backgrounds match each curated palette", () => {
  assert.equal(usesSidebarMaterial("light"), true);
  assert.equal(usesSidebarMaterial("dark"), true);
  assert.equal(usesSidebarMaterial("soft"), false);
  assert.equal(usesSidebarMaterial("black"), false);

  assert.equal(windowBackgroundOf("light"), "#ffffff");
  assert.equal(windowBackgroundOf("soft"), "#f4f5f7");
  assert.equal(windowBackgroundOf("dark"), "#1e1e21");
  assert.equal(windowBackgroundOf("black"), "#070709");
});
