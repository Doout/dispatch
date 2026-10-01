import { useEffect, useRef, useState } from "react";
import { X } from "@phosphor-icons/react";
import { registerDestructiveConfirmation, type DestructiveConfirmation, type DestructiveReview } from "./destructive";
import { useDialogFocus } from "./useDialogFocus";

type Pending = { review: DestructiveReview; resolve: (value: DestructiveConfirmation) => void; reject: (error: Error) => void };

export function DestructiveConfirmations() {
  const [pending, setPending] = useState<Pending>();
  const active = useRef<Pending | undefined>(undefined);
  useEffect(() => {
    const unregister = registerDestructiveConfirmation(review => new Promise((resolve, reject) => {
      if (active.current) { reject(new Error("Finish the current confirmation first.")); return; }
      active.current = { review, resolve, reject };
      setPending(active.current);
    }));
    return () => {
      unregister();
      active.current?.reject(new Error(""));
      active.current = undefined;
    };
  }, []);
  function finish(confirmed: boolean) {
    const value = active.current;
    if (!value) return;
    active.current = undefined;
    setPending(undefined);
    if (confirmed) value.resolve({ resourceId: value.review.resourceId, action: value.review.action, expectedVersion: value.review.version, confirmName: value.review.name });
    else value.reject(new Error(""));
  }
  return pending ? <ConfirmationDialog key={`${pending.review.resourceId}:${pending.review.version}`} review={pending.review} onCancel={() => finish(false)} onConfirm={() => finish(true)} /> : null;
}

function ConfirmationDialog({ review, onCancel, onConfirm }: { review: DestructiveReview; onCancel: () => void; onConfirm: () => void }) {
  const [name, setName] = useState("");
  const ref = useDialogFocus(onCancel);
  const action = review.action === "delete" ? "Delete" : review.action === "cleanup" ? "Clean up" : review.action === "revoke" ? "Revoke credentials" : "Replace credentials";
  return <div className="dialog-layer confirm-layer destructive-layer" onMouseDown={event => { if (event.target === event.currentTarget) onCancel(); }}>
    <section ref={ref} className="resource-dialog confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="destructive-title" aria-describedby="destructive-summary">
      <header><div><h2 id="destructive-title">{action} {review.name}</h2><small>{review.resourceType} · <code>{review.resourceId}</code></small></div><button aria-label="Close confirmation" onClick={onCancel}><X size={19} /></button></header>
      <form className="dialog-body" onSubmit={event => { event.preventDefault(); if (!review.blockedReason && name === review.name) onConfirm(); }}>
        <p id="destructive-summary">{review.summary}</p>
        {review.resources.length > 0 && <ul>{review.resources.map(resource => <li key={resource}>{resource}</li>)}</ul>}
        {review.blockedReason && <p className="form-error" role="alert">{review.blockedReason}</p>}
        {!review.blockedReason && <label className="destructive-name">Type <strong>{review.name}</strong> to confirm<input autoFocus autoComplete="off" spellCheck={false} value={name} onChange={event => setName(event.target.value)} /></label>}
        <div className="dialog-actions confirm-actions"><button type="button" className="quiet-button" onClick={onCancel}>Cancel</button><button type="submit" className="danger-button" disabled={!!review.blockedReason || name !== review.name}>{action}</button></div>
      </form>
    </section>
  </div>;
}
