// Types of relay.js, for the desktop app, which imports it.

export interface Target {
  targetId: string;
  /** Set once the kernel attached. */
  sessionId?: string;
  herSession?: string;
  openerId?: string;
  /** Opened by the person, not by a page. */
  byPerson?: boolean;
}

/** How a page is let go: closed by the kernel, the browser ending, or gone by itself. */
export type Release = "close" | "shutdown" | "gone";

export declare class TargetRelay<T extends Target = Target> {
  constructor(send: (message: object) => void, name: string);
  protected readonly targets: Map<string, T>;
  protected readonly sessions: Map<string, { target: T; child?: string }>;
  protected next: number;
  protected closed: boolean;

  /** One message from the kernel: a command, as the transport parsed it. */
  receive(command: unknown): void;
  /** The kernel went away or closed the browser: every page is let go. */
  close(): void;

  protected forward(t: T, child: string | undefined, method: string, params: object): Promise<unknown>;
  protected createTarget(url: string, herSession?: string): Promise<string> | string;
  protected attachPage(t: T): Promise<void>;
  protected release(t: T, how: Release): void;
  protected activate(t: T): void;
  protected downloadBehavior(params: Record<string, unknown>): void;
  protected listTargets(params: Record<string, unknown>): object[];
  protected describe(t: T): { url: string; title: string };

  protected target(id: unknown): T;
  protected add(t: T): T;
  protected info(t: T): object;
  protected changed(t: T): void;
  protected pageEvent(t: T, method: string, params: Record<string, unknown>, child?: string): void;
  protected drop(t: T, reason: string, how: Release): void;
  protected emit(method: string, params: object, sessionId?: string): void;
}
