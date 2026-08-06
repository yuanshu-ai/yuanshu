import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "@yuanshu/ui/styles.css";
import "./app.css";
import { LanguageProvider } from "./i18n";
import { ThemeProvider } from "./theme/ThemeProvider";

const isAdmin = window.location.pathname === "/admin" || window.location.pathname.startsWith("/admin/");
const root = createRoot(document.getElementById("root")!);

void (isAdmin ? import("./admin/AdminApp") : import("./App")).then((module) => {
  const Page = isAdmin ? (module as typeof import("./admin/AdminApp")).AdminApp : (module as typeof import("./App")).WorkbenchApp;
  root.render(<StrictMode><ThemeProvider><LanguageProvider><Page /></LanguageProvider></ThemeProvider></StrictMode>);
});
