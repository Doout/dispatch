import { ReactNode } from "react";

export function TableIconAction({ label, tooltip, danger = false, onClick, children }: { label: string; tooltip: string; danger?: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" className={`table-icon-action${danger ? " delete-action" : ""}`} aria-label={label} data-tooltip={tooltip} onClick={onClick}>{children}</button>;
}

export function StatusLabel({ state }: { state: string }) {
  return <span className={`status-label ${state}`}><i />{state}</span>;
}
