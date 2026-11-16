# Her on a PC: the tests on a real Windows PC

For whoever tries a release on a Windows PC. [windows.md](windows.md) §15 says what was built and what CI already checks on `windows-latest`. This list is what only a real PC shows: a person at it, a real Chrome, real apps, a real model.

The outcome is a report (§4): each test passed, failed or not run, with what was seen when it failed.

## 1. Before starting

- **A Windows 11 PC**, x64 or Arm, with an administrator account (the trusted device's setup asks for one, [windows.md](windows.md) §7). A second account without Git for Windows, or a second PC, for H7.
- **The release's Setup.exe**: `Hermesh-windows-x64-setup.exe`, or `Hermesh-windows-arm64-setup.exe` on Arm. Write its version in the report.
- **Another device of the owner's** set up already, an iPhone or a Mac, to approve this PC and to reach Her from (H2, H9).
- **A model** Her can use: a ChatGPT sign-in, or a key of a provider the console lists.
- **Google Chrome** installed (H5), and **Git for Windows** on the first account (H6).
- When something fails: the time, what was on screen, and `%LOCALAPPDATA%\Hermesh\logs\` (hermeshd's log) and `%USERPROFILE%\.her\logs\` (her's), zipped.

## 2. The tests

| # | Test | Steps | Passes when |
|---|---|---|---|
| H1 | Install | Run Setup.exe. SmartScreen warns once ("More info", "Run anyway"). | The app opens; Start lists Hermesh. |
| H2 | Her on this PC | In the app: Her › set up. This PC is a choice (not "not supported"). Turn it on and approve on the other device. | Her's conversation opens and Her answers. Settings › Machines shows Her running on this PC. |
| H3 | A conversation | Connect a model in the conversation. Ask something, then something that needs a web search. | Answers stream in; the search's sources show. |
| H4 | Files | Send Her a .docx, a .xlsx, a .pdf and a picture. Ask her to make a short .pptx and a .docx. | She reads each; what she makes opens in the app's preview, and opens in Word or PowerPoint once saved. |
| H5 | The browser | Ask her to look something up on a site. Then, in the console's guide, add the Hermesh extension to your Chrome and ask her to use a tab you have open. | Her own browser opens the page. With the extension, she reads and clicks in your tab. Chrome shows the extension connected. |
| H6 | Commands (Git for Windows) | Ask her to list a project's files, run `git status`, and write a small script and run it. Then ask her to delete a folder in the project. | Each runs; paths are right (no `C:Users…`). The deletion asks first unless you chose full access. |
| H7 | Commands (no Git for Windows) | On the account or PC without Git for Windows, H6 again. | She uses PowerShell; output is readable (Chinese too). **Every deletion asks, even with full access.** |
| H8 | Computer use | Open Notepad. Ask her to type a paragraph in it, then to read back what is there. | She asks to use Notepad once; the text appears; she reads it back. Windows asks for no permission. |
| H9 | From the other device | On the iPhone or Mac, open Her on this PC and talk; send a file both ways. | As on this PC. |
| H10 | Sleep and restart | Put the PC to sleep for a minute, wake it. Then restart Windows and sign in. | Her comes back by herself each time: the other device shows her online again within a minute. |
| H11 | A machine without the app | On another PC (or after H13): `irm https://github.com/hermeshai/hermesh/releases/download/<version>/install.ps1 \| iex`, approve on the other device, then turn Her on for it from the other device. | Her runs there: H3 and H6 pass from the other device. |
| H12 | Update | Once the next release is out: Hermesh › check for updates, install, relaunch. | Her is on and remembers the conversation. |
| H13 | Off, erase, uninstall | Turn Her off with "erase her data". Then Settings › Apps › Hermesh › Uninstall. | Her's folder (`%USERPROFILE%\.her`) is gone after the erase; nothing of Hermesh runs after the uninstall. |

## 3. What is known not to work yet

- Saved model keys are in a file only this user reads, not behind Windows' credential protection (DPAPI).
- A PC that is only a machine (H11) has no computer use, and opens no PDF: the app carries those, `install.ps1` does not.
- One end-to-end test still fails in CI: a session Her starts on her own PC while the relay is away. If H10 shows Her missing news from her own PC's sessions after a sleep, note it.

## 4. The report

One line per test: `H<n> passed | failed | not run`, the version and PC (Windows edition, x64 or Arm), and for a failure what was seen, when, and the logs of §1.
