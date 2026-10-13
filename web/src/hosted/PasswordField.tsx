import { useId, useState } from "react";
import { Eye, EyeSlash } from "@phosphor-icons/react";
export function PasswordField({
  label,
  value,
  onChange,
  newPassword = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  newPassword?: boolean;
}) {
  const id = useId();
  const [visible, setVisible] = useState(false);
  return (
    <div className="account-password-field">
      <label htmlFor={id}>{label}</label>
      <div className="account-password-input">
        <input
          id={id}
          type={visible ? "text" : "password"}
          autoComplete={newPassword ? "new-password" : "current-password"}
          value={value}
          minLength={newPassword ? 12 : undefined}
          onChange={(event) => onChange(event.target.value)}
          required
        />
        <button
          type="button"
          aria-label={`${visible ? "Hide" : "Show"} ${label.toLowerCase()}`}
          aria-controls={id}
          onClick={() => setVisible((value) => !value)}
        >
          {visible ? (
            <EyeSlash size={19} aria-hidden="true" />
          ) : (
            <Eye size={19} aria-hidden="true" />
          )}
        </button>
      </div>
    </div>
  );
}
