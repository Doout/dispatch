import { useCallback, useState, useSyncExternalStore } from "react";

const changed = "dispatch-interface-change";
const temporary = new Map<string, boolean>();
export const interfacePreferenceKey = (identityID: string) => `dispatch.interface.v1:${identityID}`;

function read(key: string | undefined) {
  if (!key) return false;
  if (temporary.has(key)) return temporary.get(key)!;
  try { return window.localStorage.getItem(key) === "new"; }
  catch { return false; }
}

export function useInterfacePreference(identityID?: string) {
  const key = identityID ? interfacePreferenceKey(identityID) : undefined;
  const [saveError, setSaveError] = useState<{ key: string; message: string }>();
  const subscribe = useCallback((notify: () => void) => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === key || event.key === null) { if (key) temporary.delete(key); notify(); }
    };
    window.addEventListener("storage", onStorage);
    window.addEventListener(changed, notify);
    return () => { window.removeEventListener("storage", onStorage); window.removeEventListener(changed, notify); };
  }, [key]);
  const enabled = useSyncExternalStore(subscribe, () => read(key), () => false);
  const setEnabled = useCallback((value: boolean) => {
    if (!key) return;
    try {
      window.localStorage.setItem(key, value ? "new" : "current");
      temporary.delete(key);
      setSaveError(undefined);
    } catch {
      temporary.set(key, value);
      setSaveError({ key, message: "Interface changed for this tab. Your browser could not save the setting for your next visit." });
    }
    window.dispatchEvent(new Event(changed));
  }, [key]);
  return { enabled, setEnabled, saveError: saveError && saveError.key === key ? saveError.message : "" };
}
