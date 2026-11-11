# Publishing on the Chrome Web Store

Design basis: `docs/her-server.md`. This is what to fill in when submitting, and the steps; for field names, go by what the store dashboard shows when you submit.

## 1. Steps

1. **Developer account**: register in the Chrome Web Store developer dashboard (one-time fee), fill in the publisher name and contact email, and verify the email.
2. **Privacy policy**: put section 5 at a public address (the website or GitHub Pages), and note the address.
3. **Package**: first raise `version` in `manifest.json` (the store does not accept a version number it already has), then, from the repository's root:

   ```bash
   node her/apps/chrome-extension/scripts/pack.mjs
   ```

   The output is `her/apps/chrome-extension/dist/hermesh-extension-<version>.zip`. If you have the private key that matches `key` in `manifest.json`, add `--key <private-key.pem>` for the **first** upload, so the store has a chance to keep the existing ID `igeolfcjdhlfopmimahmhleloehmfkdc` (after uploading, check that the ID matches).
4. **Create a new item**, upload the zip and fill it in following sections 2–4; the assets are in section 6. The name and the short description come from the package (`manifest.json` and `_locales/`), in English and Chinese.
5. **Visibility**: choose "Unlisted". It does not show up in search, but anyone with the link can install it in one click, and it updates automatically. Change this later to make it public.
6. **Submit for review**. `debugger` and `nativeMessaging` are sensitive permissions, so review may take longer than for an ordinary extension.
7. **Once approved**:
   - If the store's ID differs from the one above, put it in `STORE_EXTENSION_ID` in `her/crates/her-cli/src/cmd_browser.rs`, so the host manifest allows it;
   - Put the item's address in `STORE_URL` in the console's Browser section (`YourBrowser.tsx`), so the guide's step becomes an "Add to Chrome" button;
   - Both changes ship with the next Hermesh release. **Wait until the extension is live in the store before releasing that version**: the button in the console has to point to an item that already exists.

## 2. Store listing text

**Name**: Hermesh

**Short description** (≤ 132 characters; from `extDescription` in `_locales/`)

- en: Lets Her, the assistant in Hermesh, work in tabs it opens in your Chrome, with your sign-ins. It never sees your other tabs.
- zh_CN: 让 Hermesh 内置的助手 Her 在你的 Chrome 里打开标签页，使用你的登录状态；它看不到你自己的标签页。

**Long description** (en)

> Hermesh runs AI agents on your own machines, and Her, its built-in assistant, on the ones you choose. This extension lets Her use your own Chrome on the computer Her runs on, so it works with the sites you are already signed in to.
>
> - It needs Hermesh on the same Mac, with Her turned on for that Mac; on its own it does nothing.
> - Her's tabs open in a separate window, in a tab group named after the conversation. It only ever controls the tabs it opened, the tabs those open, and a tab you hand to it from the extension's menu. Your other tabs are never attached or read.
> - Chrome shows "Hermesh started debugging this browser" while Her works. Closing that bar takes control back at once, until you allow it again in Hermesh. When a task ends Her lets go of its tabs and they stay open for you.
> - When a page needs you, such as a sign-in, the task waits, and you tell it when you are done, from the extension's menu or in Hermesh.
> - Actions on public sites ask for your approval in Hermesh first.
> - The extension talks only to Hermesh on your Mac, through Chrome's native messaging. It has no server of its own and loads no remote code.

**Long description** (zh_CN)

> Hermesh 在你自己的电脑上运行 AI agent，并在你选定的电脑上运行它内置的助手 Her。这个扩展让 Her 用 Her 所在那台 Mac 上你自己的 Chrome 完成网页任务，直接使用你已经登录的网站。
>
> - 需要同一台 Mac 上装有 Hermesh，并为这台 Mac 打开了 Her；单独安装不起作用。
> - Her 的标签页开在单独的窗口里，放在以对话命名的标签组中。它只控制自己打开的标签页、由这些标签页打开的标签页，以及你从扩展菜单交给它的标签页；你自己的其他标签页从不附加，也不读取。
> - Her 工作时，Chrome 顶部会显示"Hermesh 已开始调试此浏览器"，关掉这条提示就立即收回控制，直到你在 Hermesh 里重新允许。任务结束后 Her 放开标签页，标签页留给你。
> - 页面需要你处理时（比如登录），任务会等你，你处理好后在扩展菜单或 Hermesh 里说一声就行。
> - 在公网站点上的操作，会先在 Hermesh 里征求你的同意。
> - 扩展只通过 Chrome 的 Native Messaging 与这台 Mac 上的 Hermesh 通信，没有自己的服务器，也不加载远程代码。

**Category**: Tools (one of Productivity / Developer Tools; go by the categories the dashboard has at the time)

## 3. Single purpose

> Let Her, the assistant in Hermesh, open, read and operate browser tabs on the user's behalf, in the user's own Chrome on the computer Her runs on.

## 4. Permission justifications

| Permission | Justification (submit in English) |
|---|---|
| `debugger` | Reads and operates the pages of tabs Her opened (accessibility tree, screenshots, clicks and typing) through the Chrome DevTools Protocol. Attached only to Her's own tabs; never to tabs the user opened. Chrome shows its debugging bar while attached, and closing it ends control. |
| `nativeMessaging` | Connects to Hermesh on the same Mac (host `com.hermesh.browser`, which Hermesh registers while Her is on for that Mac), which sends Her's commands and receives the results. This is the extension's only connection. |
| `tabs` | Opens, activates and closes Her's tabs, and notices when one of them opens another tab, so that tab is Her's too. |
| `tabGroups` | Puts Her's tabs in a group named after the conversation, coloured by whether it is working or waiting for the user, so the user can tell them apart. |
| `alarms` | Reconnects to Hermesh every 30 seconds if the connection dropped, and applies a pending update while Her is not using a tab. |
| `storage` | Remembers which of Her's tabs belong to which conversation while the browser runs, so Her finds them again after a restart of the extension or of Hermesh, and keeps a random id that tells this browser profile from another. |
| `identity`, `identity.email` | Reads the email of the signed-in browser profile, which Hermesh shows so the user can choose between two profiles that both have the extension. It is sent nowhere else. |

**Remote code**: No. All code is in the extension package.

## 5. Privacy and data use

**Data use disclosure** (checkboxes in the dashboard; tick what actually applies):

- Data handled: **Website content** (text, accessibility tree and screenshots in Her's tabs), **Web history** (addresses and titles of Her's tabs), **Personally identifiable information** (the browser profile's email address, used to tell profiles apart). Personal information the user types on these pages may also appear in the page content.
- This data goes only to Hermesh on the same Mac; the extension itself sends no data to any server.
- Certify: no selling of the data; no use for purposes unrelated to the single purpose; no use for credit assessment or lending.

**Draft privacy policy** (to put at a public address; the person in charge approves it, with a legal review if needed):

> **Hermesh Chrome Extension Privacy Policy**
>
> The Hermesh extension connects your Chrome to Hermesh on the same Mac, so Her, the assistant in Hermesh, can work in tabs it opens.
>
> **What the extension handles.** For tabs Her opened, tabs those tabs opened, and a tab you hand to it, the extension reads page content, such as text, the accessibility tree and screenshots, along with the tabs' addresses and titles, and performs the clicks and typing Her asks for. It does not read or attach to any other tab. It also reads the email of the signed-in browser profile, so Hermesh can tell two profiles apart.
>
> **Where it goes.** The extension sends this data only to Hermesh on your Mac, through Chrome's native messaging. The extension has no server and sends nothing over the network itself. Her may send page content to the AI model provider you set up for it in Hermesh, under that provider's terms, in order to carry out your request.
>
> **What is kept.** The extension keeps which of Her's tabs belong to which conversation while the browser runs, and a random id for this browser profile. Hermesh keeps Her's conversations, which may include page content, on that Mac, where you can delete them.
>
> **Sharing.** We do not sell this data, use it for advertising, or use it for any purpose other than carrying out your requests in Hermesh.
>
> **Control.** Closing Chrome's "is debugging this browser" bar ends Her's control of its tabs. Removing the extension, turning it off in Her's console (Browser), or turning Her off for the Mac ends it completely.
>
> **Contact.** `<contact email>`

## 6. Assets

| Asset | Spec | Status |
|---|---|---|
| Icon | 128×128 PNG | Ready: `icons/128.png`, the Mac app's icon (`apps/desktop/build/AppIcon.icon`, compiled by `actool`) |
| Screenshots | 1280×800 or 640×400, at least 1 | To do: tabs in a tab group + the conversation in Hermesh's Her entry; "In use" in the console's Browser section |
| Small promo tile | 440×280 | Optional |

## 7. Every later update

1. Raise `version` in `manifest.json`, run `node her/apps/chrome-extension/scripts/pack.mjs`, and upload the new package in the dashboard.
2. When a change touches what her requires of the extension (new CDP methods, the frame format, the behavior of browser-level methods), follow the order in D-219: **first release an extension that speaks the new bridge version, wait for the store to roll it out, then release the Hermesh that requires it**.
