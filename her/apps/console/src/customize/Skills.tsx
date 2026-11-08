import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { PageTop } from "../components";
import { Icon } from "../icons";
import { SkillPage } from "./Details";
import { MySkills, type SkillActions } from "./Lists";
import { brokenSkills, nextInLine } from "./model";
import { Loading, useEscBack } from "./parts";
import { BrokenSkills, UploadSkill } from "./UploadSkill";
import { useCustomize } from "./useCustomize";

// Skills: the SKILL.md packages of this environment, each opening a page with its files; added
// by uploading an archive, turned on or off, removed.

type Sheet = "upload" | "broken";

export function SkillsPage({ item, go }: { item: string | null; go: (item?: string) => void }) {
  const { t } = useTranslation("customize");
  const { t: tc } = useTranslation();
  const c = useCustomize();
  const [sheet, setSheet] = useState<Sheet | null>(null);
  const skill = item ? (c.items.find((i) => i.dir === item) ?? null) : null;
  useEffect(() => {
    if (item && c.loaded && !skill) go();
  }, [item, c.loaded, skill, go]);
  useEscBack(!!skill, () => go());

  const a: SkillActions = {
    open: (i) => go(i.dir),
    upload: () => setSheet("upload"),
    showBroken: () => setSheet("broken"),
    toggle: (i, on) => {
      const done = on ? t("toast.skillOn", { name: i.entry.name }) : nextInLine(c.skills, i) ? t("toast.skillOffNext", { name: i.entry.name }) : t("toast.skillOff", { name: i.entry.name });
      void c.switchSkill(i, on, done);
    },
  };

  return (
    <>
      <PageTop title={tc("nav.skills")} back={skill ? { label: tc("nav.skills"), go: () => go() } : undefined}>
        {/* With none of the person's own, the empty group offers it. */}
        {!skill && c.items.some((i) => i.group === "user") && (
          <button className="btn" onClick={a.upload}>
            <Icon name="upload" />
            {t("add.skill")}
          </button>
        )}
      </PageTop>
      <div className="set-body cz">{!c.loaded ? <Loading c={c} /> : skill ? <SkillPage key={skill.dir} c={c} a={a} item={skill} /> : <MySkills c={c} a={a} />}</div>
      {sheet === "upload" && <UploadSkill c={c} onClose={() => setSheet(null)} onAdded={() => go()} />}
      {sheet === "broken" && <BrokenSkills broken={brokenSkills(c.skills)} onClose={() => setSheet(null)} />}
    </>
  );
}
