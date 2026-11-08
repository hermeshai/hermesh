import { i18n } from "./i18n";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@hermesh/ui/brand.css";
import "./styles/base.css";
import "./styles/console.css";
import "./styles/customize.css";
import "./styles/settings.css";
import { App } from "./App";
import { HerProvider } from "./her";

document.documentElement.lang = i18n.language;
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <HerProvider>
      <App />
    </HerProvider>
  </StrictMode>,
);
