import React from "react";
import ReactDOM from "react-dom/client";
import { DestructiveConfirmations } from "./DestructiveConfirmations";
import { HostedGate } from "./hosted/HostedGate";
import "./base.css";

const App = React.lazy(() => import("./App"));

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
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
