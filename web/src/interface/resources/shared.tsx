import { ReactNode, useEffect, useState } from "react";

export function useInventory<T>(load: () => Promise<T[]>, scope: string) {
  const [items, setItems] = useState<T[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [version, setVersion] = useState(0);
  useEffect(() => {
    let current = true;
    setItems([]); setError(""); setLoading(true);
    load().then(value => { if (current) setItems(Array.isArray(value) ? value : []); })
      .catch(cause => { if (current) setError(errorMessage(cause)); })
      .finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
  }, [scope, version]);
  return { items, error, loading, refresh: () => setVersion(value => value + 1) };
}

export function errorMessage(cause: unknown) { return cause instanceof Error ? cause.message : "The request failed. Try again."; }
export function dateLabel(value?: string) { return value ? new Date(value).toLocaleString() : "Never"; }
export function InventoryStatus({ loading, error, retry }: { loading: boolean; error: string; retry: () => void }) {
  return <>{loading && <p role="status">Loading...</p>}{error && <div className="resources-error" role="alert"><p>{error}</p><button type="button" className="quiet-button" onClick={retry}>Retry</button></div>}</>;
}

export function ResourceRows<T>({ items, columns, row, rowKey, empty, label }: { items: T[]; columns: string[]; row: (item: T) => ReactNode; rowKey: (item: T) => string; empty: string; label: string }) {
  const [page, setPage] = useState(0);
  const pages = Math.max(1, Math.ceil(items.length / 20));
  const current = Math.min(page, pages - 1);
  if (!items.length) return <p className="section-empty">{empty}</p>;
  return <><div className="resources-table-scroll"><table className="resources-table" aria-label={label}><thead><tr>{columns.map(column => <th key={column} scope="col">{column}</th>)}</tr></thead><tbody>{items.slice(current * 20, current * 20 + 20).map(item => <tr key={rowKey(item)}>{row(item)}</tr>)}</tbody></table></div><div className="resources-pagination"><span>{current * 20 + 1} to {Math.min((current + 1) * 20, items.length)} of {items.length}</span><div><button className="quiet-button" type="button" disabled={current === 0} onClick={() => setPage(current - 1)}>Previous</button><button className="quiet-button" type="button" disabled={current + 1 === pages} onClick={() => setPage(current + 1)}>Next</button></div></div></>;
}
