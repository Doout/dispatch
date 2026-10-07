import { useState } from "react";
import { request } from "../../api";
import { StatusLabel } from "../../ResourceTable";
import { automationClient, Receipt } from "./client";
import { dateLabel, errorMessage } from "./shared";

export function Receipts() {
  const [id, setId] = useState("");
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [operation, setOperation] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <>
    <p>Find a request receipt by ID. You can inspect receipts submitted by your current account.</p>
    <form className="resources-form" aria-label="Find receipt" onSubmit={event => { event.preventDefault(); setBusy(true); setError(""); setReceipt(null); setOperation(undefined); void automationClient.receipt(id.trim()).then(setReceipt).catch(cause => setError(errorMessage(cause))).finally(() => setBusy(false)); }}><label>Receipt ID<input required maxLength={128} value={id} onChange={event => setId(event.target.value)} autoComplete="off" /></label><button className="primary-button" disabled={busy || !id.trim()}>{busy ? "Loading..." : "Find receipt"}</button></form>
    {error && <p className="form-error" role="alert">{error}</p>}
    {receipt && <section className="resources-detail" aria-label="Receipt details"><div className="resources-heading"><h2>{receipt.action}</h2><StatusLabel state={receipt.state} /></div>{receipt.message && <p>{receipt.message}</p>}<dl className="resources-facts"><div><dt>Receipt</dt><dd>{receipt.id}</dd></div><div><dt>Project</dt><dd>{receipt.projectId}</dd></div><div><dt>Operation</dt><dd>{receipt.operationId || "Pending acceptance"}</dd></div><div><dt>Created</dt><dd>{dateLabel(receipt.createdAt)}</dd></div><div><dt>Updated</dt><dd>{dateLabel(receipt.updatedAt)}</dd></div><div><dt>Retry deadline</dt><dd>{dateLabel(receipt.retryUntil)}</dd></div></dl>{receipt.recoveryActions?.length > 0 && <p>Recovery actions: {receipt.recoveryActions.map(action => action.replaceAll("_", " ")).join(", ")}.</p>}{receipt.operationUrl?.startsWith("/api/v1/") && !receipt.operationUrl.includes("..") && <button className="quiet-button" type="button" disabled={busy} onClick={() => { setBusy(true); setError(""); void request(receipt.operationUrl!).then(setOperation).catch(cause => setError(errorMessage(cause))).finally(() => setBusy(false)); }}>Inspect operation</button>}{operation !== undefined && <pre className="resources-json">{JSON.stringify(operation, null, 2)}</pre>}</section>}
  </>;
}
