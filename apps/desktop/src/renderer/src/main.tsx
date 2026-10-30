import "./i18n";
import { I18nextProvider } from "react-i18next";
import { createRoot } from "react-dom/client";
import { i18n } from "../../shared/i18n";
import { App } from "./App";
import "./styles.css";

// Titles go grey while another window has the focus (the .blurred rules).
window.hermesh?.onWindowFocus((focused) => document.documentElement.classList.toggle("blurred", !focused));

createRoot(document.getElementById("root")!).render(
  <I18nextProvider i18n={i18n}>
    <App />
  </I18nextProvider>,
);
