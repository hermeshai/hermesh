import "./i18n";
import { I18nextProvider } from "react-i18next";
import { createRoot } from "react-dom/client";
import { i18n } from "../../shared/i18n";
import { App } from "./App";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <I18nextProvider i18n={i18n}>
    <App />
  </I18nextProvider>,
);
