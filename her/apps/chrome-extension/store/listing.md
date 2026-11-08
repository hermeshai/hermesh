# Publishing on the Chrome Web Store

Design basis: `docs/kernel/browser-design.md` §8.4.6 (D-219). This is what to fill in when submitting, and the steps; for field names, go by what the store dashboard shows when you submit.

## 1. Steps

1. **Developer account**: register in the Chrome Web Store developer dashboard (one-time fee), fill in the publisher name and contact email, and verify the email.
2. **Privacy policy**: put section 5 at a public address (the website or GitHub Pages), and note the address.
3. **Package**: first raise `version` in `manifest.json` (the store does not accept a version number it already has), then:

   ```bash
   pnpm --filter @her/chrome-extension store-zip
   ```

   The output is in `apps/chrome-extension/dist/`. If you have the private key that matches `key` in `manifest.json`, add `--key <private-key.pem>` for the **first** upload, so the store has a chance to keep the existing ID `igeolfcjdhlfopmimahmhleloehmfkdc` (this is how the old docs did it; after uploading, check that the ID matches).
4. **Create a new item**, upload the zip and fill it in following sections 2–4; the assets are in section 6.
5. **Visibility**: choose "Unlisted". It does not show up in search, but anyone with the link can install it in one click, and it updates automatically. Change this later to make it public.
6. **Submit for review**. `debugger` and `nativeMessaging` are sensitive permissions, so review may take longer than for an ordinary extension.
7. **Once approved**:
   - If the store's ID differs from the one above, put it in `STORE_EXTENSION_ID` in `crates/her-cli/src/cmd_browser.rs`, so the host manifest allows it;
   - Put the item's address in `STORE_URL` in `apps/desktop/src/renderer/src/settings/BrowserSettings.tsx`, so step 2 of the guide becomes an "Add to Chrome" button;
   - Both changes ship with the next Hermesh release. **Wait until the extension is live in the store before releasing that version**: the button in the app has to point to an item that already exists.

## 2. Store listing text

**Name**: Hermesh

**Short description** (≤ 132 characters)

- en: Lets the Hermesh desktop agent work in tabs it opens in your Chrome, with your sign-ins. It never sees your other tabs.
- zh_CN: 让 Hermesh 桌面端的智能体在你的 Chrome 里打开标签页，使用你的登录状态；它看不到你自己的标签页。

**Long description** (en)

> Hermesh is a desktop AI agent. This extension lets it use your own Chrome for web tasks, so it works with the sites you are already signed in to.
>
> - It needs the Hermesh desktop app on the same computer; on its own it does nothing.
> - The agent's tabs open in a separate window, in a tab group named after the conversation. It only ever controls the tabs it opened, the tabs those open, and a tab you hand to it from the extension's menu. Your other tabs are never attached or read.
> - Chrome shows "Hermesh started debugging this browser" while the agent works. Closing that bar takes control back at once, until you allow it again in Hermesh. When a task ends the agent lets go of its tabs and they stay open for you.
> - When a page needs you, such as a sign-in, the task waits, and you tell it when you are done, from the extension's menu or in Hermesh.
> - Actions on public sites ask for your approval in Hermesh first.
> - The extension talks only to the Hermesh app on your computer, through Chrome's native messaging. It has no server of its own and loads no remote code.

**Long description** (zh_CN)

> Hermesh 是一个桌面端 AI 智能体。这个扩展让它用你自己的 Chrome 完成网页任务，直接使用你已经登录的网站。
>
> - 需要同一台电脑上装有 Hermesh 桌面端，单独安装不起作用。
> - 智能体的标签页开在单独的窗口里，放在以对话命名的标签组中。它只控制自己打开的标签页、由这些标签页打开的标签页，以及你从扩展菜单交给它的标签页；你自己的其他标签页从不附加，也不读取。
> - 智能体工作时，Chrome 顶部会显示"Hermesh 已开始调试此浏览器"，关掉这条提示就立即收回控制，直到你在 Hermesh 里重新允许。任务结束后智能体放开标签页，标签页留给你。
> - 页面需要你处理时（比如登录），任务会等你，你处理好后在扩展菜单或 Hermesh 里说一声就行。
> - 在公网站点上的操作，会先在 Hermesh 里征求你的同意。
> - 扩展只通过 Chrome 的 Native Messaging 与本机的 Hermesh 应用通信，没有自己的服务器，也不加载远程代码。

**Category**: Tools (one of Productivity / Developer Tools; go by the categories the dashboard has at the time)

## 3. Single purpose

> Let the Hermesh desktop app's agent open, read and operate browser tabs on the user's behalf, in the user's own Chrome.

## 4. Permission justifications

| Permission | Justification (submit in English) |
|---|---|
| `debugger` | Reads and operates the pages of tabs the agent opened (accessibility tree, screenshots, clicks and typing) through the Chrome DevTools Protocol. Attached only to the agent's own tabs; never to tabs the user opened. Chrome shows its debugging bar while attached, and closing it ends control. |
| `nativeMessaging` | Connects to the Hermesh desktop app on the same computer (host `com.hermesh.browser`), which sends the agent's commands and receives the results. This is the extension's only connection. |
| `tabs` | Opens, activates and closes the agent's tabs, and notices when one of them opens another tab, so that tab is the agent's too. |
| `tabGroups` | Puts the agent's tabs in a group named after the conversation, coloured by whether it is working or waiting for the user, so the user can tell them apart. |
| `alarms` | Reconnects to the desktop app every 30 seconds if the connection dropped, and applies a pending update while the agent is not using a tab. |
| `storage` | Remembers which of the agent's tabs belong to which conversation while the browser runs, so the agent finds them again after a restart of the extension or the app, and keeps a random id that tells this browser profile from another. |
| `identity`, `identity.email` | Reads the email of the signed-in browser profile, which the Hermesh app on the same computer shows so the user can choose between two profiles that both have the extension. It is sent nowhere else. |

**Remote code**: No. All code is in the extension package.

## 5. Privacy and data use

**Data use disclosure** (checkboxes in the dashboard; tick what actually applies):

- Data handled: **Website content** (text, accessibility tree and screenshots in the agent's tabs), **Web history** (addresses and titles of the agent's tabs), **Personally identifiable information** (the browser profile's email address, used to tell profiles apart). Personal information the user types on these pages may also appear in the page content.
- This data goes only to the Hermesh app on the same computer; the extension itself sends no data to any server.
- Certify: no selling of the data; no use for purposes unrelated to the single purpose; no use for credit assessment or lending.

**Draft privacy policy** (to put at a public address; the person in charge approves it, with a legal review if needed):

> **Hermesh Chrome Extension Privacy Policy**
>
> The Hermesh extension connects your Chrome to the Hermesh desktop app on the same computer, so the app's AI agent can work in tabs it opens.
>
> **What the extension handles.** For tabs the agent opened, tabs those tabs opened, and a tab you hand to it, the extension reads page content, such as text, the accessibility tree and screenshots, along with the tabs' addresses and titles, and performs the clicks and typing the agent asks for. It does not read or attach to any other tab. It also reads the email of the signed-in browser profile, so the app can tell two profiles apart.
>
> **Where it goes.** The extension sends this data only to the Hermesh app on your computer, through Chrome's native messaging. The extension has no server and sends nothing over the network itself. The Hermesh app may send page content to the AI model provider you configured in it, under that provider's terms, in order to carry out your request.
>
> **What is kept.** The extension keeps which of the agent's tabs belong to which conversation while the browser runs, and a random id for this browser profile. The Hermesh app keeps conversation records, which may include page content, on your computer, where you can delete them.
>
> **Sharing.** We do not sell this data, use it for advertising, or use it for any purpose other than carrying out your requests in Hermesh.
>
> **Control.** Closing Chrome's "is debugging this browser" bar ends the agent's control of its tabs. Removing the extension, or stopping it in Hermesh's settings, ends it completely.
>
> **Contact.** `<contact email>`

## 6. Assets

| Asset | Spec | Status |
|---|---|---|
| Icon | 128×128 PNG | Ready: `icons/128.png` |
| Screenshots | 1280×800 or 640×400, at least 1 | To do: tabs in a tab group + the conversation in the desktop app; "In use" in Settings › Browser |
| Small promo tile | 440×280 | Optional |

## 7. Every later update

1. Raise `version` in `manifest.json`, run `pnpm --filter @her/chrome-extension store-zip`, and upload the new package in the dashboard.
2. When a change touches what her requires of the extension (new CDP methods, the frame format, the behavior of browser-level methods), follow the order in D-219: **first release an extension that speaks the new bridge version, wait for the store to roll it out, then release the Hermesh that requires it**.
