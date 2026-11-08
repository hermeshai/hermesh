import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageTop } from "../components";
import { errorText } from "../format";
import { useConfig, useHer } from "../her";

// Instructions: what every assistant of this environment is told, besides its own
// (`instructions.extra` of her's user file, which her adds to every assistant's instructions).

export function InstructionsPage() {
  const { t } = useTranslation();
  const her = useHer();
  const { config, failed, read } = useConfig();
  /** What the file says; null until read. */
  const extra = (config?.instructions as { extra?: unknown } | undefined)?.extra;
  const saved = config === null ? null : typeof extra === "string" ? extra : "";
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  // What the file says now shows, unless the person is in the middle of a change.
  const [base, setBase] = useState<string | null>(null);
  if (saved !== base) {
    if (base === null || text === base) setText(saved ?? "");
    setBase(saved);
  }
  const dirty = saved !== null && text !== saved;

  const save = async () => {
    setBusy(true);
    try {
      const value = text.trim() ? text : null;
      await her.call("config/write", { layer: "user", edits: [{ key_path: "instructions.extra", value }] });
      await read();
      if (!value) setText("");
      her.show(t(value ? "instructions.saved" : "instructions.cleared"));
    } catch (e) {
      her.show(errorText(e), undefined, true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageTop title={t("nav.instructions")}>
        {dirty && (
          <button className="btn" disabled={busy} onClick={() => setText(saved ?? "")}>
            {t("instructions.revert")}
          </button>
        )}
        <button className="btn primary" disabled={!dirty || busy} onClick={() => void save()}>
          {t("save")}
        </button>
      </PageTop>
      <div className="set-body">
        {failed && saved === null ? (
          <p className="set-note warn">{t("failedRead", { error: failed })}</p>
        ) : (
          <div className="form instructions">
            <textarea
              className="field ins-text"
              aria-label={t("nav.instructions")}
              placeholder={t("instructions.placeholder")}
              value={text}
              disabled={saved === null}
              spellCheck={false}
              onChange={(e) => setText(e.target.value)}
            />
            <p className="set-note">{t("instructions.note")}</p>
          </div>
        )}
      </div>
    </>
  );
}
