import { useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { SkillEntry, SkillsInstallResult } from "@hermesh/her-protocol";
import { Dialog } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";
import { upload } from "../routes";
import { SkillTile } from "./parts";
import type { Customize } from "./useCustomize";

// Upload Skill: an archive, chosen or dropped, goes up to
// the Her Server; her reads it first (skills/install with dry_run), then copies it into
// ~/.her/skills. A SKILL.md alone is not offered: her installs one with the folder it is in,
// and an upload's folder on the machine holds other uploads.

type Stage = { kind: "pick"; error?: string } | { kind: "reading" } | { kind: "found"; from: string; found: SkillsInstallResult };

export function UploadSkill({ c, onClose, onAdded }: { c: Customize; onClose: () => void; onAdded: () => void }) {
  const { t } = useTranslation("customize");
  const { rpc, show } = useHer();
  const [stage, setStage] = useState<Stage>({ kind: "pick" });
  const [over, setOver] = useState(false);
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  const read = async (file: File | undefined) => {
    if (!file || !rpc) return;
    setStage({ kind: "reading" });
    try {
      const from = await upload(file, file.name);
      const found = await rpc.request("skills/install", { from, scope: "user", dry_run: true, source: { kind: "upload" } });
      setStage({ kind: "found", from, found });
    } catch (e) {
      setStage({ kind: "pick", error: errorText(e) });
    }
  };

  const add = async () => {
    if (stage.kind !== "found" || !rpc) return;
    setBusy(true);
    try {
      const done = await rpc.request("skills/install", { from: stage.from, scope: "user", replace: !!stage.found.conflict, source: { kind: "upload" } });
      await c.readSkills();
      onAdded();
      onClose();
      show(stage.found.conflict ? t("toast.skillReplaced", { name: done.skill.name }) : t("toast.skillAdded", { name: done.skill.name }));
    } catch (e) {
      setStage({ kind: "pick", error: errorText(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog label={t("upload.title")} width={520} className="cz-sheet" onClose={onClose}>
      <h3>{t("upload.title")}</h3>
      {stage.kind === "found" ? (
        <>
          <p className="sub">{t("upload.found")}</p>
          <div className="cz-parsed">
            <SkillTile />
            <div className="pt">
              <b className="mono">{stage.found.skill.name}</b>
              <p>{stage.found.skill.description}</p>
            </div>
            <span className="badge ok">
              <Icon name="check" className="sm" />
              {t("upload.valid")}
            </span>
          </div>
          <div className="cz-dlabel">{t("upload.copyTo")}</div>
          <div className="cz-target">
            <Icon name="read" />
            <span className="mono">{stage.found.target}</span>
          </div>
          {stage.found.conflict && (
            <div className="cz-callout warn cz-gap">
              <Icon name="warn" />
              <span>{t("upload.conflict")}</span>
            </div>
          )}
          <div className="sheet-foot">
            <div className="left">
              <button className="link" onClick={() => setStage({ kind: "pick" })}>
                {t("upload.another")}
              </button>
            </div>
            <button className="btn" onClick={onClose}>
              {t("upload.cancel")}
            </button>
            <button className="btn primary" disabled={busy} onClick={() => void add()}>
              {busy && <span className="mini-spin cz-spin-solid" />}
              {stage.found.conflict ? t("upload.replace") : t("upload.add")}
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="sub">
            <Trans t={t} i18nKey="upload.sub" components={{ code: <span className="mono" /> }} />
          </p>
          <div
            className={`cz-drop ${over ? "over" : ""}`}
            onDragOver={(e) => (e.preventDefault(), setOver(true))}
            onDragLeave={() => setOver(false)}
            onDrop={(e) => {
              e.preventDefault();
              setOver(false);
              // A folder can't go up as one file; its archive can.
              if (e.dataTransfer.items[0]?.webkitGetAsEntry()?.isDirectory) return setStage({ kind: "pick", error: t("upload.folder") });
              void read(e.dataTransfer.files[0]);
            }}
          >
            {stage.kind === "reading" ? (
              <>
                <span className="mini-spin" />
                <small>{t("upload.reading")}</small>
              </>
            ) : (
              <>
                <Icon name="upload" />
                <b>{t("upload.drop")}</b>
                <small>{t("upload.dropHint")}</small>
                <button className="btn cz-pick" onClick={() => input.current?.click()}>
                  {t("upload.pick")}
                </button>
                <input
                  ref={input}
                  type="file"
                  accept=".zip,.skill"
                  hidden
                  aria-label={t("upload.pick")}
                  onChange={(e) => {
                    void read(e.target.files?.[0]);
                    e.target.value = "";
                  }}
                />
              </>
            )}
          </div>
          {stage.kind === "pick" && stage.error && <div className="cz-errbox cz-gap">{stage.error}</div>}
          <div className="sheet-foot">
            <button className="btn" onClick={onClose}>
              {t("upload.cancel")}
            </button>
          </div>
        </>
      )}
    </Dialog>
  );
}

/** The skills that did not load, and why. */
export function BrokenSkills({ broken, onClose }: { broken: SkillEntry[]; onClose: () => void }) {
  const { t } = useTranslation("customize");
  return (
    <Dialog label={t("broken.title", { count: broken.length })} width={520} className="cz-sheet" onClose={onClose}>
      <h3>{t("broken.title", { count: broken.length })}</h3>
      <p className="sub">{t("broken.sub")}</p>
      <div className="cz-broken">
        {broken.map((b) => (
          <div key={b.path} className="fgroup">
            <div className="frow">
              <Icon name="file" />
              <span className="fv mono">{b.path}</span>
            </div>
            <div className="frow">
              <span className="fl">{t("broken.reason")}</span>
              <span className="fv">{b.detail}</span>
            </div>
          </div>
        ))}
      </div>
      <div className="sheet-foot">
        <button className="btn" onClick={onClose}>
          {t("broken.close")}
        </button>
      </div>
    </Dialog>
  );
}
