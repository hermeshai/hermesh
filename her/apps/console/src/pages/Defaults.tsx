import { useTranslation } from "react-i18next";
import type { ConfigEdit, PermissionMode, ThinkingLevel } from "@hermesh/her-protocol";
import { PageTop } from "../components";
import { errorText } from "../format";
import { useConfig, useHer } from "../her";
import { approvals, modelChoices, startsWith, thinkingChoices } from "./settings";

// Defaults: the model, thinking level and approval mode a new assistant of this environment
// starts with (`model`, `thinking`, `permission_mode` of her's user file). Each assistant
// changes its own in its settings, in the app.

export function DefaultsPage({ go }: { go: (page: "models") => void }) {
  const { t } = useTranslation();
  const her = useHer();
  const { models } = her;
  const { config, read } = useConfig();
  const now = startsWith(models, config);
  const choices = modelChoices(models, now.model);
  const model = models?.models.find((m) => m.id === now.model);
  const levels = thinkingChoices(model);
  const providers = [...new Set(choices.map((m) => m.provider))];
  const providerName = (id: string) => models?.providers.find((p) => p.id === id)?.name ?? id;
  const ready = !!models && config !== null;

  const write = async (edits: ConfigEdit[]) => {
    try {
      await her.call("config/write", { layer: "user", edits });
      await read();
      her.show(t("defaults.saved"));
    } catch (e) {
      her.show(errorText(e), undefined, true);
    }
  };
  const setModel = (id: string) => {
    const next = models?.models.find((m) => m.id === id);
    // A level the new model does not offer goes back to the provider's own choice.
    const keep = thinkingChoices(next).includes(now.thinking);
    void write([{ key_path: "model", value: id }, ...(keep ? [] : [{ key_path: "thinking", value: "default" }])]);
  };
  const mode = now.permission_mode;

  return (
    <>
      <PageTop title={t("nav.defaults")} />
      <div className="set-body">
        <div className="form">
          <section>
            <div className="fgroup">
              <div className="frow">
                <div className="ft">
                  <label className="fn" htmlFor="defaults-model">
                    {t("defaults.model")}
                  </label>
                </div>
                {!ready ? (
                  <span className="mini-spin" />
                ) : choices.length ? (
                  <select id="defaults-model" className="psel" value={now.model ?? ""} onChange={(e) => setModel(e.target.value)}>
                    {!now.model && <option value="" disabled />}
                    {providers.map((p) => (
                      <optgroup key={p} label={providerName(p)}>
                        {choices
                          .filter((m) => m.provider === p)
                          .map((m) => (
                            <option key={m.id} value={m.id}>
                              {m.name}
                            </option>
                          ))}
                      </optgroup>
                    ))}
                  </select>
                ) : (
                  <span className="fv-side">
                    {t("defaults.noModel")}
                    <button className="link" onClick={() => go("models")}>
                      {t("defaults.connect")}
                    </button>
                  </span>
                )}
              </div>
              <div className="frow">
                <div className="ft">
                  <label className="fn" htmlFor="defaults-thinking">
                    {t("defaults.thinking")}
                  </label>
                </div>
                <select id="defaults-thinking" className="psel" value={now.thinking} disabled={!ready || levels.length < 2} onChange={(e) => void write([{ key_path: "thinking", value: e.target.value as ThinkingLevel }])}>
                  {levels.map((l) => (
                    <option key={l} value={l}>
                      {t(`thinking.${l}`)}
                    </option>
                  ))}
                </select>
              </div>
              <div className="frow">
                <div className="ft">
                  <label className="fn" htmlFor="defaults-approval">
                    {t("defaults.approval")}
                  </label>
                  <div className="fdesc">{t(`permission.${mode}.desc`)}</div>
                </div>
                <select id="defaults-approval" className="psel" value={mode} disabled={!ready} onChange={(e) => void write([{ key_path: "permission_mode", value: e.target.value as PermissionMode }])}>
                  {approvals.map((p) => (
                    <option key={p} value={p}>
                      {t(`permission.${p}.title`)}
                    </option>
                  ))}
                  {/* Set elsewhere: shown, not offered. */}
                  {!approvals.includes(mode) && (
                    <option value={mode} disabled>
                      {t(`permission.${mode}.title`)}
                    </option>
                  )}
                </select>
              </div>
            </div>
            <p className="set-note">{t("defaults.note")}</p>
          </section>
        </div>
      </div>
    </>
  );
}
