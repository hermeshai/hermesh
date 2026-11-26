import { useEffect, useState } from "react";
import { herChoices } from "./her/environments";
import { Done } from "./firstrun/Done";
import { HerAwake, HerChoose, awake } from "./firstrun/HerStep";
import { Network } from "./firstrun/Network";
import { firstRunDone, setFirstRunDone, setProgress, useFirstRunDone, useProgress, type Step } from "./firstrun/progress";
import { ErasedNote, Removed } from "./firstrun/SignIn";
import { SignInFlow, type Way } from "./firstrun/SignInFlow";
import { Stage, type Aura } from "./firstrun/Stage";
import { Searching, Team, useSearching } from "./firstrun/Team";
import { Welcome } from "./firstrun/Welcome";
import { setNotNow } from "./start";
import { save, useStore } from "./store";

/**
 * The first run (docs/cold-start-prd.md §5), in the compact window and in ink: the welcome, this
 * Mac's agent providers, Her, signing in (which can wait), done. No account is needed before the
 * fourth step, nor at all. Also the page of a computer another device removed.
 */
export function FirstRun() {
  const st = useStore();
  if (st.status?.removed) return <Removed />;
  return <ColdStart />;
}

function ColdStart() {
  const st = useStore();
  const s = st.status;
  const progress = useProgress();
  const step: Step = progress?.step ?? "welcome";
  const m = st.machines.find((x) => x.local);
  const go = (next: Step) => setProgress({ step: next });
  const [way, setWay] = useState<Way | null>(null);
  const searching = useSearching(m, step === "team");
  // Her runs here, or can: this Mac is one she can run on (docs/cold-start-prd.md §5.5).
  const choice = m && herChoices([m])[0];
  const herHere = !!m && (!!m.her?.enabled || choice?.why === null);
  const woke = awake(m);
  const signedIn = !!s?.initialized && !s.signedOut;

  const enter = () => {
    // What "Remind me" did on the authorization page before (§5.8): open at login, and notify.
    for (const k of ["openAtLogin", "notifyQuestion", "notifyFailed"]) save(k, true);
    void window.hermesh?.setLoginItem(true);
    // Without Her here, the main window opens on the overview, not on Her's own first run.
    if (!herHere) setNotNow(true);
    setFirstRunDone(true);
    setProgress(null);
  };

  let aura: Aura;
  let page: string;
  let body;
  switch (step) {
    case "welcome":
      aura = "welcome";
      page = "welcome";
      body = <Welcome start={() => go("team")} note={<ErasedNote />} />;
      break;
    case "team":
      aura = "team";
      page = searching || !m ? "team:searching" : "team:found";
      body = searching || !m ? <Searching /> : <Team m={m} next={() => go(herHere ? "her" : "far")} />;
      break;
    case "her":
      aura = woke ? "awake" : "her";
      page = woke ? "her:awake" : "her:choose";
      body = !m ? null : woke ? <HerAwake m={m} next={() => go("far")} /> : <HerChoose m={m} next={() => go("far")} />;
      break;
    case "far":
      aura = signedIn ? "linked" : "far";
      page = signedIn ? "far:in" : "far:out";
      body = <Network signedIn={signedIn} signIn={() => setWay("official")} own={() => setWay("own")} next={() => go("done")} />;
      break;
    case "done":
      aura = "done";
      page = "done";
      body = <Done her={herHere} model={woke} enter={enter} />;
      break;
  }
  return (
    <Stage aura={aura} page={page} sheet={way && <SignInFlow way={way} close={() => setWay(null)} done={() => setWay(null)} />}>
      {body}
    </Stage>
  );
}

/**
 * Whether the first run shows: a step of it is kept, or this computer has never finished it and
 * belongs to no one yet. A computer set up before it existed counts as finished (useFirstRunMarks).
 */
export function useFirstRunPending(): boolean {
  const st = useStore();
  const s = st.status;
  const progress = useProgress();
  const done = useFirstRunDone();
  if (progress) return true;
  if (!s) return false;
  return !s.initialized && !s.machineOnly && !s.signedOut && !s.removed && !done;
}

/** Keeps the finished mark true to what hermeshd says: set for a computer set up already, forgotten once it was erased. */
export function useFirstRunMarks() {
  const st = useStore();
  const s = st.status;
  const progress = useProgress();
  useEffect(() => {
    if (!s || progress) return;
    if ((s.initialized || s.machineOnly) && !firstRunDone()) setFirstRunDone(true);
    if (s.erased && !s.initialized && firstRunDone()) setFirstRunDone(false);
  }, [s?.initialized, s?.machineOnly, s?.erased, progress]);
}
