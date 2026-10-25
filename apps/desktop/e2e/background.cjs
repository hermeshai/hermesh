// Loaded before the app in every test launch (electron -r): windows open
// behind everything else without taking focus, so the tests don't take over
// the Mac they run on. Playwright drives pages over CDP, which needs neither.
const { app, BrowserWindow } = require("electron");

BrowserWindow.prototype.show = function () {
  this.showInactive();
  this.blur(); // On macOS this orders the window to the back.
};
BrowserWindow.prototype.focus = function () {};
app.focus = () => {};
