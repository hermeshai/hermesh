import { Connect } from "./firstrun/Connect";
import { FirstTask } from "./firstrun/FirstTask";
import { Prepare } from "./firstrun/Prepare";
import { useProgress } from "./firstrun/progress";
import { Removed, SignIn } from "./firstrun/SignIn";
import { useStore } from "./store";

/**
 * The first run, in the compact window: sign in (or a relay of one's own,
 * or join an owner), then set up this Mac, connect an agent and wait for
 * its first task. Also the page of a computer another device removed.
 */
export function FirstRun() {
  const st = useStore();
  const progress = useProgress();
  if (st.status?.removed) return <Removed />;
  if (!st.status?.initialized) return <SignIn />;
  if (progress?.step === "prepare") return <Prepare />;
  if (progress?.step === "agent") return <Connect />;
  if (progress?.step === "first") return <FirstTask agent={progress.agent} />;
  return null;
}

/** The first run's pages after signing in are not done yet (the main window waits). */
export function useFirstRunPending(): boolean {
  return useProgress() !== null;
}
