import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import SessionApp from "./codex/App";
import "./styles.css";
import "./codex/styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <SessionApp />
  </StrictMode>,
);
