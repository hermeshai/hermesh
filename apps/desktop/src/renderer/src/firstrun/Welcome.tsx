import { useEffect, useRef } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Mark } from "@hermesh/ui";
import { fx } from "./fx";
import { rise } from "./Stage";

// 1 Welcome (docs/cold-start-prd.md §5.3): the mark, gathered from particles and then drawn,
// one line on what Hermesh is, one button. No account, no server.

export function Welcome({ start, note }: { start: () => void; note?: React.ReactNode }) {
  const { t } = useTranslation("firstrun");
  const mark = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    fx.assemble(mark.current?.querySelector("svg") ?? null);
  }, []);
  return (
    <div className="fr-main">
      {note}
      <div className="fr-center fr-welcome">
        <span ref={mark}>
          <Mark size={92} variant="draw" delay={1000} />
        </span>
        <h1 className="fr-big in" style={rise(0, 1900)}>
          {t("intro.title")}
        </h1>
        <p className="fr-lede in" style={rise(0, 2300)}>
          <Trans t={t} i18nKey="intro.lede" components={{ br: <br /> }} />
        </p>
        <button className="btn primary lg in" style={{ ...rise(0, 2800), marginTop: 36 }} onClick={start}>
          {t("intro.start")}
        </button>
      </div>
    </div>
  );
}
