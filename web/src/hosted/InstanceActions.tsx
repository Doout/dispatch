import { useRef, useState, type FormEvent } from "react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { DotsThree, Plug, Trash, X } from "@phosphor-icons/react";
import { useDialogFocus } from "../useDialogFocus";
import { hostedRequest, type Membership } from "./client";

export type InstanceAction = "disconnect" | "delete";

export function InstanceActionsMenu({
  membership,
  onSelect,
}: {
  membership: Membership;
  onSelect: (action: InstanceAction) => void;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const selecting = useRef(false);
  const select = (action: InstanceAction) => {
    selecting.current = true;
    trigger.current?.focus();
    onSelect(action);
  };
  if (
    membership.role !== "owner" ||
    (!membership.canDelete &&
      membership.connection?.backendMode !== "connected")
  )
    return null;
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          className="quiet-button hosted-instance-actions"
          aria-label={`Actions for ${membership.tenant.name}`}
          ref={trigger}
        >
          <DotsThree size={20} aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="hosted-instance-menu"
          align="end"
          sideOffset={6}
          collisionPadding={12}
          onCloseAutoFocus={(event) => {
            if (selecting.current) {
              event.preventDefault();
              selecting.current = false;
            }
          }}
        >
          {membership.connection?.backendMode === "connected" &&
            membership.connection.state !== "not_connected" && (
              <DropdownMenu.Item onSelect={() => select("disconnect")}>
                <Plug size={17} aria-hidden="true" /> Disconnect server
              </DropdownMenu.Item>
            )}
          <DropdownMenu.Item
            className="hosted-instance-delete"
            onSelect={() => select("delete")}
          >
            <Trash size={17} aria-hidden="true" /> Delete instance
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function InstanceActionDialog({
  membership,
  action,
  onClose,
  onCompleted,
}: {
  membership: Membership;
  action: InstanceAction;
  onClose: () => void;
  onCompleted: () => Promise<void>;
}) {
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const close = () => {
    if (!busy) onClose();
  };
  const ref = useDialogFocus(close);
  const deleting = action === "delete";
  const allowed =
    membership.role === "owner" &&
    (membership.connection?.backendMode === "connected" ||
      (deleting && membership.canDelete));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (
      !allowed ||
      busy ||
      (deleting && confirmation !== membership.tenant.slug)
    )
      return;
    setBusy(true);
    setError("");
    try {
      const path = `/tenants/${encodeURIComponent(membership.tenant.id)}`;
      await hostedRequest(
        deleting ? path : `${path}/connection`,
        "DELETE",
        deleting ? { slug: confirmation } : undefined,
      );
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
      return;
    }
    onClose();
    await onCompleted();
  };
  if (!allowed) return null;
  return (
    <div className="hosted-instance-dialog-backdrop">
      <section
        className="hosted-instance-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="instance-action-title"
        aria-describedby="instance-action-description"
        ref={ref}
      >
        <header>
          <h2 id="instance-action-title">
            {deleting
              ? `Delete ${membership.tenant.name}?`
              : `Disconnect ${membership.tenant.name}?`}
          </h2>
          <button
            className="icon-button"
            aria-label="Close confirmation"
            disabled={busy}
            onClick={close}
          >
            <X size={20} aria-hidden="true" />
          </button>
        </header>
        <form onSubmit={(event) => void submit(event)}>
          <div className="dialog-body hosted-form">
            <p id="instance-action-description">
              {deleting
                ? membership.connection?.backendMode === "connected"
                  ? "This removes the instance and its member access from Doodship. Its address and webhooks will stop working. Your server, data and running jobs stay intact."
                  : "This removes the instance and its member access from Doodship. Its stored data is kept for recovery."
                : "This stops access through Doodship and webhook forwarding. Your server, local accounts and running jobs stay available. You can reconnect later."}
            </p>
            {deleting && (
              <label htmlFor="instance-delete-name">
                <span>
                  Type <strong>{membership.tenant.slug}</strong> to delete this
                  instance.
                </span>
                <input
                  id="instance-delete-name"
                  value={confirmation}
                  disabled={busy}
                  onChange={(event) => setConfirmation(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              </label>
            )}
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
          </div>
          <footer>
            <button
              type="button"
              className="quiet-button"
              disabled={busy}
              onClick={close}
              data-autofocus={!deleting ? true : undefined}
            >
              Cancel
            </button>
            <button
              className="danger-button"
              disabled={
                busy || (deleting && confirmation !== membership.tenant.slug)
              }
            >
              {busy
                ? deleting
                  ? "Deleting..."
                  : "Disconnecting..."
                : deleting
                  ? "Delete instance"
                  : "Disconnect"}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}
