import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App";
import "./app.css";
import { LanguageProvider, LanguageSwitch } from "./i18n";
import { ThemeProvider } from "./theme/ThemeProvider";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider><LanguageProvider><LanguageSwitch /><App /></LanguageProvider></ThemeProvider>
  </StrictMode>,
);
