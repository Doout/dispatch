import { FormEvent, useState } from "react";
import { Key, X } from "@phosphor-icons/react";
import { api } from "./api";
import { useDialogFocus } from "./useDialogFocus";

export function ChangePasswordDialog({ onClose }: { onClose: () => void }) {
  const dialogRef = useDialogFocus(onClose);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError("");
    if (newPassword !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api.changePassword(currentPassword, newPassword);
      onClose();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };

  return <div className="dialog-layer access-dialog-layer" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialogRef} className="resource-dialog password-dialog" role="dialog" aria-modal="true" aria-labelledby="password-dialog-title">
      <header><div className="password-dialog-title"><Key size={20} /><h2 id="password-dialog-title">Change password</h2></div><button aria-label="Close dialog" onClick={onClose}><X size={19} weight="bold" /></button></header>
      <div className="dialog-body">
        <form className="password-form" onSubmit={submit} aria-busy={busy}>
          <label><span>Current password</span><input type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} autoComplete="current-password" autoFocus required disabled={busy} /></label>
          <label><span>New password</span><input type="password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} autoComplete="new-password" minLength={12} required disabled={busy} /><small>12 characters minimum.</small></label>
          <label><span>Confirm new password</span><input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" minLength={12} required disabled={busy} /></label>
          {error && <p className="form-error" role="alert">{error}</p>}
          <div className="dialog-actions"><button type="button" className="quiet-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? "Changing..." : "Change password"}</button></div>
        </form>
      </div>
    </section>
  </div>;
}
