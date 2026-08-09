import React from "react";
import ReactDOM from "react-dom/client";

import App from "./App";
import "./index.css";

// 跟随系统主题。这类工具会被长时间挂着，深色不是锦上添花。
const dark = window.matchMedia("(prefers-color-scheme: dark)");
const applyTheme = (isDark: boolean) =>
  document.documentElement.classList.toggle("dark", isDark);
applyTheme(dark.matches);
dark.addEventListener("change", (e) => applyTheme(e.matches));

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
