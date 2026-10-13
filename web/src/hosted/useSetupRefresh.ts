import { useEffect } from "react";

export function instanceNeedsRefresh(state: string) {
  return state === "pending" || state === "provisioning" || state === "failed";
}

// Setup runs asynchronously. Keep its status current until it finishes,
// without overlapping requests or polling an idle, hidden page.
export function useSetupRefresh(
  enabled: boolean,
  refresh: () => Promise<void>,
) {
  useEffect(() => {
    if (!enabled) return;
    let stopped = false;
    let timer: number;
    const schedule = () => {
      if (!stopped) timer = window.setTimeout(poll, 5000);
    };
    const poll = () => {
      if (document.visibilityState !== "hidden") {
        void refresh().then(schedule, schedule);
      } else schedule();
    };
    schedule();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [enabled, refresh]);
}
