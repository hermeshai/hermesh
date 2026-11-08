// An open file in the file panel (docs/desktop/file-panel-design.md §6, §7, §10): what is on
// disk, what is being edited from it, and how the edits get back there. Saves go one at a
// time, after a pause in typing or at once when asked; each names the version it was edited
// from, so one the disk has moved on from writes nothing and waits for the person to choose.

/** Where an open file's edits are. */
export type SaveState =
  /** The text is what is on disk. */
  | "clean"
  /** Edited, not saved yet. */
  | "dirty"
  | "saving"
  /** The file on disk is no longer the version the text was edited from. */
  | "conflict"
  /** The file was removed while the text was being edited. */
  | "deleted"
  /** A save failed (no permission, a full disk); the next edit tries again. */
  | "failed"
  /** Not to be written: her's own data, or not UTF-8 text. */
  | "refused";

/** How `files/write` answered. */
export type WriteResult = { status: "saved" | "conflict" | "refused"; hash?: string | null; message?: string | null };

/** A version of the file as it is on disk. */
export type DiskVersion = { text: string; hash: string };

export type FileDocOptions = {
  /** Writes `text`, as long as the file is still the version `base` names (null: whatever is there). */
  write: (text: string, base: string | null) => Promise<WriteResult>;
  /** Called whenever the state, the text or the disk's version changes. */
  changed: () => void;
  /** How long typing pauses before a save; 1 s. */
  delay?: number;
  /** Timers, replaceable in tests. */
  setTimer?: (f: () => void, ms: number) => unknown;
  clearTimer?: (t: unknown) => void;
};

export class FileDoc {
  /** The version the text was edited from: what the next save names. */
  disk: DiskVersion;
  /** The text as it is being edited. */
  text: string;
  state: SaveState = "clean";
  /** Why a save failed or was refused. */
  message: string | null = null;
  /** Grows when the text is replaced from outside the editor (the disk's version taken): the editor takes it then. */
  replaced = 0;
  /** When the last save went through (ms since the epoch); null before any. */
  savedAt: number | null = null;
  private readonly opts: FileDocOptions;
  private timer: unknown = null;
  private running: Promise<void> | null = null;
  /** The text changed while a save was out: save again once it is back. */
  private again = false;

  constructor(disk: DiskVersion, opts: FileDocOptions) {
    this.disk = disk;
    this.text = disk.text;
    this.opts = opts;
  }

  get dirty(): boolean {
    return this.text !== this.disk.text;
  }

  /** Whether edits are saved on their own: not while the person has a choice to make. */
  private get stuck(): boolean {
    return this.state === "conflict" || this.state === "deleted" || this.state === "refused";
  }

  /** The editor's text now; saved after the pause. */
  edit(text: string) {
    if (text === this.text) return;
    this.text = text;
    if (!this.stuck && this.state !== "saving") this.state = this.dirty ? "dirty" : "clean";
    if (this.state === "failed") this.state = "dirty";
    this.opts.changed();
    if (!this.stuck && this.dirty) this.schedule();
  }

  private schedule() {
    this.cancel();
    const set = this.opts.setTimer ?? ((f, ms) => setTimeout(f, ms));
    this.timer = set(() => {
      this.timer = null;
      void this.save();
    }, this.opts.delay ?? 1000);
  }

  private cancel() {
    if (this.timer === null) return;
    (this.opts.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>)))(this.timer);
    this.timer = null;
  }

  /** Saves now what is not saved yet (⌘S, closing, leaving): resolves once it is written or cannot be. */
  flush(): Promise<void> {
    this.cancel();
    if (this.stuck) return Promise.resolve();
    if (this.running) {
      if (this.dirty) this.again = true;
      return this.running;
    }
    return this.dirty ? this.save() : Promise.resolve();
  }

  private save(force = false): Promise<void> {
    if (this.running) {
      this.again = true;
      return this.running;
    }
    if (!force && (this.stuck || !this.dirty)) return Promise.resolve();
    const text = this.text;
    this.state = "saving";
    this.message = null;
    this.opts.changed();
    this.running = (async () => {
      let result: WriteResult;
      try {
        result = await this.opts.write(text, force ? null : this.disk.hash);
      } catch (e) {
        this.running = null;
        this.state = "failed";
        this.message = e instanceof Error ? e.message : String(e);
        this.opts.changed();
        return;
      }
      this.running = null;
      if (result.status === "saved" && result.hash) {
        this.disk = { text, hash: result.hash };
        this.savedAt = Date.now();
        this.state = this.dirty ? "dirty" : "clean";
      } else if (result.status === "conflict") {
        this.state = result.hash ? "conflict" : "deleted";
        this.again = false;
      } else {
        this.state = "refused";
        this.message = result.message ?? null;
        this.again = false;
      }
      this.opts.changed();
      // Typed while it was out: those edits go next, on the new base.
      if (this.again && this.state === "dirty") {
        this.again = false;
        await this.save();
      }
    })();
    return this.running;
  }

  /** "Save anyway": this text over whatever the disk has now. */
  keepMine(): Promise<void> {
    this.cancel();
    this.state = "dirty";
    return this.save(true);
  }

  /** "Load the disk's version": the edits here are dropped. */
  takeDisk(version: DiskVersion) {
    this.cancel();
    this.again = false;
    this.disk = version;
    this.text = version.text;
    this.state = "clean";
    this.message = null;
    this.replaced++;
    this.opts.changed();
  }

  /**
   * The file changed on disk (§7): read again, or gone (null). An unedited file takes the new
   * version; an edited one waits for the person to choose. The version this side just saved
   * changes nothing.
   */
  diskChanged(version: DiskVersion | null) {
    if (version && version.hash === this.disk.hash) return;
    if (this.state === "saving") return;
    if (!version) {
      this.cancel();
      this.state = "deleted";
      this.opts.changed();
      return;
    }
    if (!this.dirty) return this.takeDisk(version);
    this.cancel();
    this.state = "conflict";
    this.opts.changed();
  }
}
