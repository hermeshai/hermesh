import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Mark } from "@hermesh/ui";
import { Cat } from "../her/Cat";
import { catColors, fx } from "./fx";
import { rise } from "./Stage";

// 5 Done (docs/cold-start-prd.md §5.7): confetti from both lower corners, and the way in.

export function Done({ her, model, enter }: { her: boolean; model: boolean; enter: () => void }) {
  const { t } = useTranslation("firstrun");
  const face = useRef<HTMLDivElement>(null);
  useEffect(() => {
    fx.later(() => fx.confetti(), 300);
    if (her) fx.later(() => fx.burst(face.current, catColors, 40, 6), 520);
  }, []);
  return (
    <div className="fr-main mid">
      <div className="fr-face in" ref={face} style={rise(0)}>
        {her ? <Cat small={false} busy={model} mood={model ? undefined : "asleep"} /> : <Mark size={92} />}
      </div>
      <h2 className="fr-title in" style={{ ...rise(1), marginTop: 20 }}>
        {t("finish.title")}
      </h2>
      <p className="fr-text in" style={rise(2)}>
        {!her ? t("finish.noHer") : model ? t("finish.ready") : t("finish.noModel")}
      </p>
      <button className="btn primary lg in" style={{ ...rise(5), marginTop: 32 }} onClick={enter}>
        {t("finish.enter")}
      </button>
    </div>
  );
}
