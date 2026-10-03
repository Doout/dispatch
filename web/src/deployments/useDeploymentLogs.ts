import { useEffect, useState } from "react";
import { api, type Deployment, type DeploymentLog } from "../api";

export function useDeploymentLogs(selectedDeployment?: Pick<Deployment, "id" | "finishedAt">) {
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState("");
  useEffect(() => {
    if (!selectedDeployment?.id) {
      setLogs([]);
      setLogsLoading(false);
      setLogsError("");
      return;
    }
    let active = true;
    setLogs([]);
    setLogsLoading(true);
    setLogsError("");
    const refresh = async () => {
      try {
        const next = await api.logs(selectedDeployment.id);
        if (active) {
          setLogs(next);
          setLogsError("");
          setLogsLoading(false);
        }
      } catch (cause) {
        if (active) {
          setLogsError(
            (cause as Error).message || "Deployment logs are unavailable.",
          );
          setLogsLoading(false);
        }
      }
    };
    void refresh();
    const timer = selectedDeployment.finishedAt ? undefined : window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, 3000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [selectedDeployment?.id, selectedDeployment?.finishedAt]);
  return { logs, logsLoading, logsError };
}
