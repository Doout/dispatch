import { FormEvent, useState } from "react";
import { Overview } from "../../api";
import { StatusLabel } from "../../ResourceTable";
import { automationClient, AutomationAccount, Credential, Grant } from "./client";
import { dateLabel, errorMessage, InventoryStatus, ResourceRows, useInventory } from "./shared";

const permissions = ["project.view", "project.configure", "deployment.run", "deployment.cancel", "service.provision", "runtime.cleanup", "infrastructure.inspect", "infrastructure.create", "infrastructure.modify", "infrastructure.delete", "infrastructure.snapshot", "infrastructure.restore"];

export function Credentials({ overview }: { overview: Overview }) {
  const accounts = useInventory(automationClient.accounts, overview.identity?.id ?? "");
  const [selected, setSelected] = useState("");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const account = accounts.items.find(item => item.id === selected);
  async function create(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try { const saved = await automationClient.createAccount(name.trim(), description.trim()); setSelected(saved.id); setCreating(false); setName(""); setDescription(""); accounts.refresh(); }
    catch (cause) { setError(errorMessage(cause)); } finally { setBusy(false); }
  }
  return <>
    <div className="resources-heading"><p>Give CI jobs and agents an account with expiring credentials and project permissions.</p><button type="button" className="primary-button" onClick={() => setCreating(true)}>Create account</button></div>
    {error && <p role="alert" className="form-error">{error}</p>}
    {creating && <form className="resources-form" onSubmit={event => void create(event)} aria-label="Create automation account"><label>Account name<input required maxLength={120} value={name} onChange={event => setName(event.target.value)} /></label><label>Description<textarea maxLength={2000} value={description} onChange={event => setDescription(event.target.value)} /></label><div className="resources-actions"><button type="button" className="quiet-button" disabled={busy} onClick={() => setCreating(false)}>Cancel</button><button className="primary-button" disabled={busy || !name.trim()}>Create account</button></div></form>}
    <InventoryStatus {...accounts} retry={accounts.refresh} />
    {!accounts.loading && !accounts.error && <ResourceRows items={accounts.items} label="Automation accounts" columns={["Account", "State", "Actions"]} rowKey={item => item.id} empty="No automation accounts." row={item => <><td><strong>{item.name}</strong><small>{item.description || item.id}</small></td><td><StatusLabel state={item.state} /></td><td><button type="button" className="table-action" aria-pressed={item.id === selected} onClick={() => setSelected(item.id)}>Manage {item.name}</button></td></>} />}
    {account && <AccountDetails key={account.id} account={account} overview={overview} onChanged={accounts.refresh} />}
  </>;
}

function AccountDetails({ account, overview, onChanged }: { account: AutomationAccount; overview: Overview; onChanged: () => void }) {
  const credentials = useInventory(() => automationClient.credentials(account.id), account.id);
  const grants = useInventory(async () => (await automationClient.grants()).filter(item => item.principalType === "service_account" && item.principalId === account.id), account.id);
  const [issue, setIssue] = useState<Credential | "new" | null>(null);
  const [name, setName] = useState("");
  const [days, setDays] = useState(30);
  const [token, setToken] = useState("");
  const [confirmation, setConfirmation] = useState<{ label: string; action: () => Promise<unknown> } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  async function act(action: () => Promise<void>) {
    setBusy(true); setError(""); setNotice("");
    try { await action(); } catch (cause) { setError(errorMessage(cause)); } finally { setBusy(false); }
  }
  function beginIssue(value: Credential | "new") { setIssue(value); setName(value === "new" ? "" : value.name); setDays(30); setToken(""); setConfirmation(null); }
  return <section className="resources-detail" aria-label={`${account.name} account details`}>
    <div className="resources-heading"><h2>{account.name}</h2><div className="resources-actions"><button type="button" className="quiet-button" disabled={busy || account.state !== "active" || !!token} onClick={() => beginIssue("new")}>Issue credential</button><button type="button" className="quiet-button" disabled={busy || !!token} onClick={() => setConfirmation({ label: account.state === "active" ? `Disable ${account.name}? Its credentials will stop working.` : `Enable ${account.name}? Unexpired credentials will work again.`, action: () => automationClient.updateAccount(account, account.state === "active" ? "disabled" : "active") })}>{account.state === "active" ? "Disable account" : "Enable account"}</button></div></div>
    {error && <p className="form-error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    {confirmation && <div className="resources-confirm"><p>{confirmation.label}</p><div className="resources-actions"><button type="button" className="quiet-button" disabled={busy} onClick={() => setConfirmation(null)}>Cancel</button><button type="button" className="danger-button" disabled={busy} onClick={() => void act(async () => { await confirmation.action(); setConfirmation(null); credentials.refresh(); onChanged(); setNotice("Account updated."); })}>Confirm</button></div></div>}
    {issue && <form className="resources-form" aria-label={issue === "new" ? "Issue credential" : "Rotate credential"} onSubmit={event => { event.preventDefault(); void act(async () => { const saved = await automationClient.issue(account.id, name.trim(), new Date(Date.now() + days * 86400000).toISOString(), issue === "new" ? undefined : issue.id); setToken(saved.token); setIssue(null); credentials.refresh(); }); }}><label>Credential name<input required maxLength={120} value={name} onChange={event => setName(event.target.value)} /></label><label>Expires in days<input type="number" min={1} max={365} step={1} required value={days} onChange={event => setDays(Number(event.target.value))} /></label>{issue !== "new" && <p>Issuing the replacement immediately revokes the old credential.</p>}<div className="resources-actions"><button type="button" className="quiet-button" disabled={busy} onClick={() => setIssue(null)}>Cancel</button><button className="primary-button" disabled={busy || !name.trim()}>{issue === "new" ? "Issue credential" : "Rotate credential"}</button></div></form>}
    {token && <div className="resources-token" role="status"><h3>Save this token</h3><p>Dispatch shows it once. Store it in your CI or agent secret store before leaving this account.</p><label>New token<textarea readOnly value={token} autoComplete="off" spellCheck={false} /></label><button type="button" className="quiet-button" onClick={() => setToken("")}>I saved the token</button></div>}
    <InventoryStatus {...credentials} retry={credentials.refresh} />
    {!credentials.loading && !credentials.error && <ResourceRows items={credentials.items} label="Account credentials" columns={["Credential", "Expiry", "Last used", "Actions"]} rowKey={item => item.id} empty="No credentials issued." row={item => { const inactive = !!item.revokedAt || Date.parse(item.expiresAt) <= Date.now(); return <><td><strong>{item.name}</strong><small>{item.revokedAt ? "Revoked" : inactive ? "Expired" : "Active"}</small></td><td>{dateLabel(item.expiresAt)}</td><td>{dateLabel(item.lastUsedAt)}</td><td><div className="resources-actions"><button type="button" className="table-action" disabled={busy || inactive || !!token || account.state !== "active"} onClick={() => beginIssue(item)}>Rotate {item.name}</button><button type="button" className="table-action delete-action" disabled={busy || !!token || !!item.revokedAt} onClick={() => setConfirmation({ label: `Revoke ${item.name}? Requests using this token will fail.`, action: () => automationClient.revoke(account.id, item.id) })}>Revoke {item.name}</button></div></td></>; }} />}
    <h3>Project permissions</h3><InventoryStatus {...grants} retry={grants.refresh} />
    {!grants.loading && !grants.error && <GrantEditor key={account.id} account={account} overview={overview} grants={grants.items} onChanged={grants.refresh} />}
  </section>;
}

function GrantEditor({ account, overview, grants, onChanged }: { account: AutomationAccount; overview: Overview; grants: Grant[]; onChanged: () => void }) {
  const [project, setProject] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [expiry, setExpiry] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  return <><ResourceRows items={grants} label="Account project permissions" columns={["Project", "Permissions", "Expiry"]} rowKey={item => item.projectId} empty="No project permissions assigned." row={item => <><td><button type="button" className="table-action" onClick={() => { setProject(item.projectId); setSelected(item.permissions); setExpiry(item.expiresAt ? new Date(Date.parse(item.expiresAt) - new Date(item.expiresAt).getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ""); }}>{overview.projects.find(project => project.id === item.projectId)?.name || item.projectId}</button></td><td>{item.permissions.join(", ") || "None"}</td><td>{item.expiresAt ? dateLabel(item.expiresAt) : "No expiry"}</td></>} />
    <form className="resources-form" aria-label="Edit project permissions" onSubmit={event => { event.preventDefault(); setBusy(true); setError(""); setNotice(""); const grant: Grant = { principalType: "service_account", principalId: account.id, projectId: project, permissions: selected, ...(expiry ? { expiresAt: new Date(expiry).toISOString() } : {}) }; void (selected.length ? automationClient.saveGrant(grant) : automationClient.removeGrant(grant)).then(() => { onChanged(); setNotice("Project permissions saved."); }).catch(cause => setError(errorMessage(cause))).finally(() => setBusy(false)); }}>
      <label>Project<select required disabled={busy} value={project} onChange={event => { const grant = grants.find(item => item.projectId === event.target.value); setProject(event.target.value); setSelected(grant?.permissions || []); setExpiry(grant?.expiresAt ? new Date(Date.parse(grant.expiresAt) - new Date(grant.expiresAt).getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ""); }}><option value="">Choose project</option>{overview.projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <fieldset disabled={busy}><legend>Permissions</legend><div className="resources-permissions">{permissions.map(permission => <label key={permission}><input type="checkbox" checked={selected.includes(permission)} onChange={event => setSelected(event.target.checked ? [...selected, permission] : selected.filter(item => item !== permission))} />{permission}</label>)}</div></fieldset>
      <label>Grant expiry, optional<input type="datetime-local" value={expiry} onChange={event => setExpiry(event.target.value)} /></label><p>Saving replaces this account's direct permissions for the selected project. An empty selection removes its direct access.</p>
      <button className="primary-button" disabled={busy || !project}>Save permissions</button>{error && <p role="alert" className="form-error">{error}</p>}{notice && <p role="status">{notice}</p>}
    </form></>;
}
