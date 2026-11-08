// The her native protocol as TypeScript types. Everything that crosses the
// wire is generated from the kernel's schema (./generated.ts, desktop-kernel-
// integration §9.2); this file only adds names derived from those types, and
// the JSON-RPC envelope, which the schema does not describe. Field names are
// the JSON's own, snake_case: nothing is renamed on the way to the page.

import type {
  AssistantContent,
  Event,
  Message,
  Methods,
  RpcErrorKind,
  ServerNotifications,
  TranscriptItem,
} from "./generated.ts";

export * from "./generated.ts";

/** Unix time in milliseconds. */
export type Timestamp = number;

/** Distributes Omit over a union, so each variant keeps its own fields. */
type OmitEach<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;

/** An event's own part, without the sequence number and time every event carries. */
export type EventMsg = OmitEach<Event, "seq" | "ts">;
export type EventType = Event["type"];

export type AssistantMessage = Extract<Message, { role: "assistant" }>;
export type ToolCall = Extract<AssistantContent, { type: "tool_call" }>;
/** Where a task ended, in a transcript. */
export type TaskEndItem = Extract<TranscriptItem, { type: "task_end" }>;

/** Requests by method, as [params, result]. */
export type { Methods };
/** Notifications from the server, by method. */
export type Notifications = ServerNotifications;

export interface RpcErrorObject {
  code: number;
  message: string;
  data?: { kind?: RpcErrorKind | string } & Record<string, unknown>;
}

/** Params of a request without any. */
export type NoParams = Methods["models/list"][0];
