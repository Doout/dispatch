import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { DestructiveConfirmations } from "./DestructiveConfirmations";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode><App /><DestructiveConfirmations /></React.StrictMode>,
);
