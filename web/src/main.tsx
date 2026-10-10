import React from "react";
import ReactDOM from "react-dom/client";
import { DestructiveConfirmations } from "./DestructiveConfirmations";
import { HostedGate } from "./hosted/HostedGate";
import { UiPreviewToolbar, uiPreviewConfiguration } from "./UiPreviewToolbar";
import "./base.css";

const App = React.lazy(() => import("./App"));
const preview = uiPreviewConfiguration();
if (preview) document.documentElement.classList.add("ui-preview-active");

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    {preview && <UiPreviewToolbar configuration={preview} />}
    <HostedGate>
      <React.Suspense
        fallback={
          <div className="hosted-auth" role="status">
            Loading Dispatch…
          </div>
        }
      >
        <App />
        <DestructiveConfirmations />
      </React.Suspense>
    </HostedGate>
  </React.StrictMode>,
);
