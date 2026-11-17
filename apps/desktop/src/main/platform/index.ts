import { linux } from "./linux";
import { mac } from "./mac";
import type { Platform } from "./types";
import { win } from "./win";

// One implementation per system, chosen here once (docs/windows.md §5.1,
// docs/linux.md §7.1): no other file of the main process asks process.platform.
export const platform: Platform = process.platform === "win32" ? win : process.platform === "linux" ? linux : mac;
export type { Platform, WindowKind } from "./types";
