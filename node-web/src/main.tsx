import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App";
import "./app.css";
import { LanguageProvider, LanguageSwitch } from "./i18n";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <LanguageProvider><LanguageSwitch /><App /></LanguageProvider>
  </StrictMode>,
);
