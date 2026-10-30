import { app, dialog } from "electron";
import { i18n } from "../shared/i18n";

// The installed app runs from the Applications folder (docs/desktop-app.md
// §6.7). Opened anywhere else (the disk image, Downloads, where App
// Translocation runs it from a read-only copy), it registers hermeshd's
// LaunchAgent at a path that goes away and Squirrel cannot update it. So
// before anything else it asks to move there: Electron copies the bundle
// (asking for an administrator's password when the folder needs one),
// replaces an older copy that isn't running, ejects the disk image and
// opens the moved app. Declining quits.

/** True when the app may start here; otherwise it has moved, or is quitting. */
export async function settleInApplications(): Promise<boolean> {
  if (app.isInApplicationsFolder()) return true;
  const { response } = await dialog.showMessageBox({
    message: i18n.t("main:move.title"),
    detail: i18n.t("main:move.body"),
    buttons: [i18n.t("main:move.move"), i18n.t("main:move.quit")],
    defaultId: 0,
    cancelId: 1,
  });
  if (response === 0) {
    try {
      // On success it has already asked the app to quit, and the moved one opens.
      if (app.moveToApplicationsFolder()) return false;
    } catch (e) {
      console.warn("moving to Applications:", e);
    }
    await dialog.showMessageBox({ type: "warning", message: i18n.t("main:move.failedTitle"), detail: i18n.t("main:move.failedBody"), buttons: [i18n.t("main:move.ok")] });
  }
  app.quit();
  return false;
}
