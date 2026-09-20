import { useEffect, useRef, useState } from "react";
import { ArrowClockwise, ArrowRight, CheckCircle, GithubLogo, Link, Plus, UsersThree, WarningCircle } from "@phosphor-icons/react";
import { type AccessOverview, api, request } from "../api";
import "./workflows.css";

type Mapping = { id: string; providerId: string; externalGroup: string; teamId: string };
const message = (cause: unknown) => cause instanceof Error ? cause.message : String(cause);
export function IdentityMappings({ onChanged }: { onChanged?: () => void | Promise<void> }) {
  const [access, setAccess] = useState<AccessOverview>();
  const [items, setItems] = useState<Mapping[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [editing, setEditing] = useState(false);
  const [provider, setProvider] = useState("");
  const [team, setTeam] = useState("");
  const [group, setGroup] = useState("");
  const [removing, setRemoving] = useState<string>();
  const [busy, setBusy] = useState("");
  const [refresh, setRefresh] = useState(0);
  const alive = useRef(true); const action = useRef(false);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    let current = true; setLoading(true); setError("");
    Promise.all([api.access(), request<Mapping[]>("/api/v1/identity-team-mappings")]).then(([access, rows]) => { if (current) { setAccess(access); setItems(rows); } }).catch(cause => { if (current) setError(message(cause)); }).finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
  }, [refresh]);
  async function changed() { try { await onChanged?.(); } catch { if (alive.current) setNotice("Mapping changed. Refresh the overview to update its summary."); } }
  async function save() {
    if (action.current || loading || !provider || !team || !group.trim()) return;
    if (!/^[^\s/]+\/[^\s/]+$/.test(group.trim())) { setError("Use a GitHub organization/team-slug without spaces."); return; }
    action.current = true; setBusy("save"); setError(""); setNotice("");
    try {
      await request<Mapping>("/api/v1/identity-team-mappings", { method: "POST", body: JSON.stringify({ providerId: provider, teamId: team, externalGroup: group.trim() }) });
      if (alive.current) { setEditing(false); setGroup(""); setProvider(""); setTeam(""); setNotice("Identity mapping saved. Verified membership is applied at sign-in."); setRefresh(value => value + 1); await changed(); }
    } catch (cause) { if (alive.current) setError(message(cause)); }
    finally { action.current = false; if (alive.current) setBusy(""); }
  }
  async function remove(item: Mapping) {
    if (action.current || removing !== item.id) return;
    action.current = true; setBusy(item.id); setError(""); setNotice("");
    try {
      await request(`/api/v1/identity-team-mappings/${encodeURIComponent(item.id)}`, { method: "DELETE" });
      if (alive.current) { setItems(rows => rows.filter(row => row.id !== item.id)); setRemoving(undefined); setNotice("Identity mapping removed. Memberships supplied by this mapping were revoked."); await changed(); }
    } catch (cause) { if (alive.current) setError(message(cause)); }
    finally { action.current = false; if (alive.current) setBusy(""); }
  }
  const available = !!access?.providers.length && !!access.teams.length;
  return <section className="ops-task-panel operations-mappings">
    <header className="ops-task-heading"><div><h2>Identity mappings</h2><p>Connect verified GitHub team membership to an existing Dispatch team.</p></div><div className="ops-task-heading-actions"><button className="quiet-button" disabled={loading || !!busy} onClick={() => setRefresh(value => value + 1)}><ArrowClockwise size={14} />Refresh</button>{!editing && <button className="primary-button" disabled={loading || !!busy || !available} onClick={() => { setEditing(true); setRemoving(undefined); setError(""); }}><Plus size={14} />Add mapping</button>}</div></header>
    {error && <div className="ops-task-error" role="alert"><WarningCircle size={16} /><span>{error}</span>{!access && <button className="quiet-button" onClick={() => setRefresh(value => value + 1)}>Try again</button>}</div>}
    {notice && <p className="ops-task-success" role="status"><CheckCircle size={15} />{notice}</p>}
    {editing && <form className="ops-mapping-form" aria-label="Add identity mapping" onSubmit={event => { event.preventDefault(); void save(); }}><h3>Add a team mapping</h3><div className="ops-mapping-fields"><label>Sign-in provider<select aria-label="Sign-in provider" required disabled={!!busy} value={provider} onChange={event => setProvider(event.target.value)}><option value="">Choose provider</option>{access?.providers.map(item => <option key={item.id} value={item.id}>{item.name}{item.state === "disabled" ? " (disabled)" : ""}</option>)}</select></label><label>GitHub team<input aria-label="GitHub team" required maxLength={200} placeholder="organization/team-slug" disabled={!!busy} value={group} onChange={event => setGroup(event.target.value)} /></label><ArrowRight size={17} className="ops-mapping-form-arrow" /><label>Dispatch team<select aria-label="Dispatch team" required disabled={!!busy} value={team} onChange={event => setTeam(event.target.value)}><option value="">Choose team</option>{access?.teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label></div><p>Members verified at sign-in receive the mapped team's existing project access. The GitHub provider requires the read:org scope.</p><div className="ops-task-form-actions"><button type="button" className="quiet-button" disabled={!!busy} onClick={() => setEditing(false)}>Cancel</button><button className="primary-button" disabled={!!busy || !provider || !team || !group.trim()}>{busy === "save" ? "Saving…" : "Save mapping"}</button></div></form>}
    {loading && !access && <p className="ops-task-empty" role="status">Loading identity mappings…</p>}
    {access && !available && <p className="ops-mapping-help">Configure a GitHub sign-in provider and create a Dispatch team in Access before adding a mapping.</p>}
    {!loading && !items.length && access && <div className="ops-task-empty"><Link size={23} /><strong>No identity mappings yet</strong><p>Add a mapping to connect GitHub team membership with Dispatch access.</p></div>}
    {!!items.length && <ul className="ops-mapping-list">{items.map(item => {
      const provider = access?.providers.find(provider => provider.id === item.providerId);
      const team = access?.teams.find(team => team.id === item.teamId);
      return <li key={item.id}><div className="ops-mapping-row"><div className="ops-mapping-source"><GithubLogo size={19} /><div><strong>{item.externalGroup}</strong><span>{provider?.name || "Provider unavailable"}{provider?.state === "disabled" ? " · Disabled" : ""}</span></div></div><ArrowRight size={16} className="ops-mapping-arrow" /><div className="ops-mapping-target"><UsersThree size={18} /><div><strong>{team?.name || "Team unavailable"}</strong><span>Dispatch team</span></div></div><button className="quiet-button" disabled={!!busy} aria-label={`Review removal of ${item.externalGroup} mapping`} onClick={() => { setRemoving(removing === item.id ? undefined : item.id); setEditing(false); setError(""); }}>Remove</button></div>{removing === item.id && <div className="ops-mapping-removal" role="group" aria-label={`Remove mapping ${item.externalGroup}`}><WarningCircle size={19} /><div><strong>Remove {item.externalGroup} → {team?.name || "the recorded team"}?</strong><p>Memberships supplied by this mapping are revoked immediately. Manual memberships remain unchanged.</p><div className="ops-task-form-actions"><button className="quiet-button" disabled={!!busy} onClick={() => setRemoving(undefined)}>Cancel</button><button className="danger-button" disabled={!!busy} onClick={() => void remove(item)}>{busy === item.id ? "Removing…" : "Remove mapping now"}</button></div></div></div>}</li>;
    })}</ul>}
    <p className="ops-mapping-footnote">Mappings refresh verified membership at sign-in. Ownership labels and manual team memberships are managed separately.</p>
  </section>;
}
