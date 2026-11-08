import { useTranslation } from "react-i18next";
import type { ErrorInfo } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { useRowState } from "../session/useRowState";
import { useStore } from "../store";
import type { SendOp } from "./Approval";
import { useHer } from "./context";
import { failureAdvice, failureTitle, modelTitle } from "./format";
import { MenuButton, MenuItem, ModelList, SubMenuItem } from "./menus";

// A task's failure, as one row: a mark in the tone
// of what the failure asks of the owner, what happened, and one way out; the
// other ways and the provider's own words in a menu. A login that expired, or
// a key that is wrong, is fixed in the environment's console.

/** What a failure asks: set something up, wait it out, or change the request. */
type Tone = "setup" | "wait" | "fail";
const tones: Record<string, Tone> = {
  auth: "setup",
  quota_exceeded: "setup",
  context_overflow: "setup",
  rate_limited: "wait",
  overloaded: "wait",
  server: "wait",
  network: "wait",
  timeout: "wait",
};
const toneOf = (kind: string): Tone => tones[kind] ?? "fail";
const marks: Record<Tone, string> = { setup: "info", wait: "clock", fail: "alert" };

/** The conversation's current failure, with its way out; background tasks it did not stop are told of, with a way to stop them. */
export function FailureCard({ error, working, model, op, stateId }: { error?: ErrorInfo; working: number; model?: string; op: SendOp; stateId: string }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const kind = error?.kind ?? "unknown";
  const tone = toneOf(kind);
  const models = her.env.models;
  const providerId = model?.split("/")[0] ?? "";
  const provider = models.providers.find((p) => p.id === providerId)?.name || providerId || t("fail.theProvider");
  const [open, setOpen] = useRowState(stateId, false);
  const retry = () => op({ type: "continue" });
  const pick = async (id: string) => {
    if (await op({ type: "set_model", model: id })) void retry();
  };
  const copy = () => error?.message && navigator.clipboard.writeText(error.message).then(() => st.show(t("chat.copiedError")));
  const main =
    kind === "auth" ? (
      <button className="btn sm primary" onClick={her.openConsole}>
        {t("model.openConsole")}
      </button>
    ) : kind === "context_overflow" ? (
      <button className="btn sm primary" onClick={() => void op({ type: "compact" })}>
        {t("chat.compact")}
      </button>
    ) : (
      <button className="btn sm primary" onClick={() => void retry()}>
        <Icon name="retry" className="sm" />
        {t("chat.retry")}
      </button>
    );
  const retryIsMain = kind !== "auth" && kind !== "context_overflow";
  return (
    <div className="fail-wrap">
      <div className={`fcard f-${tone}`} role="alert">
        <span className="fc-av">
          <Icon name={kind === "auth" ? "key" : marks[tone]} />
        </span>
        <div className="fc-tx">
          <b>{t(`fail.${kind}` as "fail.unknown", { provider, defaultValue: t("fail.unknown") })}</b>
          <span>
            {failureAdvice(kind, error?.retryable ?? false)}
            {working > 0 && ` ${t("fail.working", { count: working })}`}
          </span>
        </div>
        <div className="fc-side">
          {working > 0 && (
            <button className="btn sm" onClick={() => void op({ type: "interrupt" })}>
              {t("fail.stopWorking")}
            </button>
          )}
          {main}
          <MenuButton className="fc-more" align="right" title={t("chat.more")} label={<Icon name="more" />}>
            {(close) => (
              <>
                <SubMenuItem label={t("chat.switchModel")} value={model ? modelTitle(model, models) : ""} title={t("chat.switchModel")}>
                  <ModelList models={models} current={model} onClose={close} onPick={(m) => (close(), void pick(m.id))} />
                </SubMenuItem>
                {!retryIsMain && <MenuItem icon="retry" label={t("chat.retry")} onClick={() => (close(), void retry())} />}
                {error?.message && (
                  <>
                    <div className="msep" role="separator" />
                    <MenuItem icon="chevR" label={open ? t("chat.hideError") : t("chat.showError")} onClick={() => (close(), setOpen(!open))} />
                    <MenuItem icon="copy" label={t("chat.copyError")} onClick={() => (close(), void copy())} />
                  </>
                )}
              </>
            )}
          </MenuButton>
        </div>
      </div>
      {open && <div className="fc-raw sel-text">{error?.message}</div>}
    </div>
  );
}

/** A failure the conversation went on from: one quiet line, its message behind it. */
export function PastFailure({ error, stateId }: { error?: ErrorInfo; stateId: string }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useRowState(stateId, false);
  const kind = error?.kind ?? "unknown";
  return (
    <div className="fail-wrap">
      <button className={`fpast f-${toneOf(kind)}`} aria-expanded={error?.message ? open : undefined} onClick={() => error?.message && setOpen(!open)}>
        <i />
        <span>{t("chat.failedPast", { title: failureTitle(kind) })}</span>
        {error?.message && <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />}
      </button>
      {open && <div className="fc-raw sel-text">{error?.message}</div>}
    </div>
  );
}
