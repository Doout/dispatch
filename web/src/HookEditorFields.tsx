import { ChangeEvent, useId, useState } from "react";
import { UploadSimple } from "@phosphor-icons/react";
import { Secret } from "./api";
import { readHookScriptFile } from "./fileUploads";

export function HookFields({ preDeployHook, postDeployHook, onPreDeployHook, onPostDeployHook }: { preDeployHook: string; postDeployHook: string; onPreDeployHook: (value: string) => void; onPostDeployHook: (value: string) => void }) {
  const id = useId();
  const [preFileName, setPreFileName] = useState("");
  const [postFileName, setPostFileName] = useState("");
  const [uploadError, setUploadError] = useState("");

  async function loadScript(event: ChangeEvent<HTMLInputElement>, onChange: (value: string) => void, onFileName: (value: string) => void) {
    const file = event.target.files?.[0];
    if (!file) return;
    setUploadError("");
    try {
      onChange(await readHookScriptFile(file));
      onFileName(file.name);
    } catch (cause) {
      setUploadError((cause as Error).message);
    } finally {
      event.target.value = "";
    }
  }

  return <div className="event-hook-fields">
    <section className="hook-script-field"><header><div><label htmlFor={`${id}-pre`}>Build and publish</label><small>Bash. Preview tag: <code>$1</code>.</small></div><label className="hook-upload"><UploadSimple size={14} /><span>Upload script</span><input className="sr-only" type="file" accept=".sh,.bash,text/x-shellscript,text/plain" onChange={(event) => void loadScript(event, onPreDeployHook, setPreFileName)} /></label></header><textarea id={`${id}-pre`} value={preDeployHook} onChange={(event) => { onPreDeployHook(event.target.value); setPreFileName(""); }} placeholder={'#!/usr/bin/env bash\nset -euo pipefail\n\ndocker buildx build --push --tag "$REGISTRY/app:$1" .'} spellCheck={false} />{preFileName && <small className="hook-file-name">{preFileName}</small>}</section>
    <section className="hook-script-field"><header><div><label htmlFor={`${id}-post`}>After deployment</label><small>Bash. Runs after the release is ready.</small></div><label className="hook-upload"><UploadSimple size={14} /><span>Upload script</span><input className="sr-only" type="file" accept=".sh,.bash,text/x-shellscript,text/plain" onChange={(event) => void loadScript(event, onPostDeployHook, setPostFileName)} /></label></header><textarea id={`${id}-post`} value={postDeployHook} onChange={(event) => { onPostDeployHook(event.target.value); setPostFileName(""); }} placeholder={'echo "Ready at $DISPATCH_DEPLOYMENT_URL"'} spellCheck={false} />{postFileName && <small className="hook-file-name">{postFileName}</small>}</section>
    {uploadError && <p className="form-error hook-upload-error" role="alert">{uploadError}</p>}
    <details className="hook-output-contract"><summary>Build output contract</summary><div><dl><div><dt>Preview tag</dt><dd><code>$1</code> and <code>$DISPATCH_PREVIEW_TAG</code></dd></div><div><dt>Named outputs</dt><dd>Saved with the deployment and passed to later hooks as <code>DISPATCH_OUTPUT_*</code>.</dd></div><div><dt>Deployment values</dt><dd>Helm applies these overrides only to this deployment.</dd></div></dl><pre>{`dispatch-hook output set backendImage "$backend_image"
dispatch-hook output set uiImage "$ui_image"
dispatch-hook output set imageTag "$preview_tag"

dispatch-hook helm set images.registry "$registry_host"
dispatch-hook helm set images.namespace "$registry_namespace"
dispatch-hook helm set images.backend.tag "$preview_tag"
dispatch-hook helm set images.ui.tag "$preview_tag"`}</pre><small>Legacy output files still work. Never publish credentials.</small></div></details>
  </div>;
}

export function HookCredentialBindings({ secrets, selected, onChange }: { secrets: Secret[]; selected: string[]; onChange: (ids: string[]) => void }) {
  return <fieldset className="event-secret-bindings"><legend>Build credentials</legend><p>Only selected credentials enter the build. <code>SSH_PRIVATE_KEY</code> also configures Git.</p>{secrets.length ? <div>{secrets.map((secret) => <label key={secret.id}><input type="checkbox" checked={selected.includes(secret.id)} onChange={(event) => onChange(event.target.checked ? [...selected, secret.id] : selected.filter((id) => id !== secret.id))} /><span><strong>{secret.name}</strong><code>{secret.environmentVariable}</code></span></label>)}</div> : <small>Add credentials on the Secrets page.</small>}</fieldset>;
}
