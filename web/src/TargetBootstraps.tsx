import { FormEvent, useEffect, useState } from "react";
import { Overview, request } from "./api";
import { StatusLabel } from "./ResourceTable";

export type BootstrapPlan = {
  targetName: string; method: "cloud_init" | "ssh"; platform: string; imageFamily: string;
  installRuntime: boolean; replaceIdentity: boolean; controllerUrl?: string; artifactUrl?: string;
  artifactSha256?: string; sshHost?: string; sshPort?: number; sshUser?: string;
  sshHostKey?: string; sshFingerprint?: string; sshVerified?: boolean; actions?: string[];
};
type Installation = { id: string; serverId: string; plan: BootstrapPlan; digest: string; state: string; installationState: string; enrollmentState: string; runtimeState: string; message: string; reviewExpiresAt: string; claimExpiresAt: string; acceptedAt?: string };
type Target = { id: string; name: string; address: string };
const root = "/api/v1/infrastructure/bootstrap";
const emptyPlan = (): BootstrapPlan => ({ targetName: "", method: "ssh", platform: "linux-amd64", imageFamily: "existing-systemd", installRuntime: false, replaceIdentity: false, sshHost: "", sshPort: 22, sshUser: "root", sshHostKey: "", sshVerified: false });
const post = <T,>(path: string, body: unknown) => request<T>(path, { method: "POST", body: JSON.stringify(body) });

export function BootstrapReview({ plan }: { plan: BootstrapPlan }) {
  return <div className="bootstrap-review">
    {plan.method === "ssh" && <><p>SSH target: <strong>{plan.sshUser}@{plan.sshHost}:{plan.sshPort}</strong></p><p>Verified host key: <code>{plan.sshFingerprint}</code></p></>}
    <p>Controller: {plan.controllerUrl}</p><p>Agent: {plan.platform} · {plan.imageFamily}</p>
    <label>Reviewed agent SHA-256<input readOnly value={plan.artifactSha256 || ""} /></label>
    <ol>{plan.actions?.map(action => <li key={action}>{action}</li>)}</ol>
    {plan.replaceIdentity && <p className="form-error">This approval replaces the target identity. Its old enrollment and agent sessions will be retired.</p>}
  </div>;
}

export function TargetBootstraps({ overview }: { overview: Overview }) {
  const [items, setItems] = useState<Installation[]>([]);
  const [managed, setManaged] = useState<Target[]>([]);
  const [open, setOpen] = useState(false);
  const [serverId, setServerId] = useState("");
  const [plan, setPlan] = useState<BootstrapPlan>(emptyPlan);
  const [authMethod, setAuthMethod] = useState("password");
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [privateKeyPassword, setPrivateKeyPassword] = useState("");
  const [review, setReview] = useState<Installation | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const targets = [...(overview.servers || []).filter(target => target.runtime === "docker" && target.address !== "local"), ...managed].filter((target, index, all) => all.findIndex(other => other.id === target.id) === index);
  const clearCredentials = () => { setPassword(""); setPrivateKey(""); setPrivateKeyPassword(""); };
  async function refresh() { const value = await request<Installation[]>(root); setItems(Array.isArray(value) ? value : []); }
  useEffect(() => {
    let active = true;
    const update = () => request<Installation[]>(root).then(value => { if (active) setItems(Array.isArray(value) ? value : []); }).catch(() => {});
    void update();
    void request<(Target & { allocationState: string })[]>("/api/v1/infrastructure/servers").then(value => { if (active && Array.isArray(value)) setManaged(value.filter(item => item.allocationState === "allocated")); }).catch(() => {});
    const timer = setInterval(() => void update(), 5000);
    return () => { active = false; clearInterval(timer); };
  }, []);
  async function act(fn: () => Promise<void>) { setBusy(true); setError(""); try { await fn(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Target installation request failed."); } finally { setBusy(false); } }
  async function prepare(event: FormEvent) {
    event.preventDefault();
    await act(async () => {
      try { const result = await post<Installation>(`${root}/review`, { serverId, plan, credentials: authMethod === "password" ? { password } : { privateKey, privateKeyPassword } }); setReview(result); setConfirmation(""); }
      finally { clearCredentials(); }
    });
  }
  async function accept(event: FormEvent) { event.preventDefault(); if (!review) return; await act(async () => { await post(`${root}/${review.id}/accept`, { digest: review.digest, confirmName: confirmation }); setReview(null); setOpen(false); await refresh(); }); }
  return <section className="server-section bootstrap-section" aria-labelledby="target-bootstrap-title">
    <div className="section-title"><div><h2 id="target-bootstrap-title">Target agent installation</h2><p>Install, upgrade or recover a Docker target through verified SSH. Routine operations use its enrolled agent.</p></div><button className="quiet-button" onClick={() => { setOpen(true); setReview(null); setPlan(emptyPlan()); setServerId(""); setConfirmation(""); clearCredentials(); }}>Install or recover agent</button></div>
    {error && <p role="alert" className="form-error">{error}</p>}
    {open && !review && <form className="connection-form inline-create" aria-label="Review target installation" onSubmit={event => void prepare(event)}>
      <div className="connection-grid">
        <label>Intended target<select value={serverId} onChange={event => { const target = targets.find(item => item.id === event.target.value); setServerId(event.target.value); setPlan({ ...plan, targetName: target?.name || "", sshHost: target?.address || "", replaceIdentity: false, sshHostKey: "", sshVerified: false }); }}><option value="">Import an existing machine</option>{targets.map(target => <option key={target.id} value={target.id}>{target.name}</option>)}</select></label>
        <label>Target name<input required maxLength={100} readOnly={Boolean(serverId)} value={plan.targetName} onChange={event => setPlan({ ...plan, targetName: event.target.value })} /></label>
        <label>SSH host<input required readOnly={Boolean(serverId)} value={plan.sshHost} onChange={event => setPlan({ ...plan, sshHost: event.target.value, sshVerified: false })} /></label>
        <label>SSH port<input required type="number" min={1} max={65535} value={plan.sshPort} onChange={event => setPlan({ ...plan, sshPort: Number(event.target.value), sshVerified: false })} /></label>
        <label>SSH user<input required value={plan.sshUser} onChange={event => setPlan({ ...plan, sshUser: event.target.value })} /><small>Non-root users need passwordless sudo for this installer.</small></label>
        <label>Agent platform<select value={plan.platform} onChange={event => setPlan({ ...plan, platform: event.target.value })}><option value="linux-amd64">Linux amd64</option><option value="linux-arm64">Linux arm64</option></select></label>
        <label>Host prerequisites<select value={plan.imageFamily} onChange={event => setPlan({ ...plan, imageFamily: event.target.value, installRuntime: event.target.value === "ubuntu-24.04" })}><option value="existing-systemd">Existing systemd, Python 3, Docker, Compose v2 and Git</option><option value="ubuntu-24.04">Ubuntu 24.04: install Docker, Compose v2 and Git</option></select></label>
        <label>Authentication<select value={authMethod} onChange={event => { setAuthMethod(event.target.value); clearCredentials(); }}><option value="password">Password</option><option value="privateKey">Private key</option></select></label>
        {authMethod === "password" ? <label>SSH password<input required type="password" autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} /></label> : <><label>SSH private key<textarea required value={privateKey} onChange={event => setPrivateKey(event.target.value)} /></label><label>Private key passphrase<input type="password" autoComplete="new-password" value={privateKeyPassword} onChange={event => setPrivateKeyPassword(event.target.value)} /></label></>}
      </div>
      <label>Verified SSH host public key<textarea required value={plan.sshHostKey} onChange={event => setPlan({ ...plan, sshHostKey: event.target.value, sshVerified: false })} /><small>Get this key or its fingerprint from the provider console or another trusted channel. A network scan alone does not verify the host.</small></label>
      <label><input required type="checkbox" checked={Boolean(plan.sshVerified)} onChange={event => setPlan({ ...plan, sshVerified: event.target.checked })} />I verified this host key through a trusted channel.</label>
      {serverId && <label><input type="checkbox" checked={plan.replaceIdentity} onChange={event => setPlan({ ...plan, replaceIdentity: event.target.checked })} />Replace the enrolled identity for recovery</label>}
      <p>Credentials are encrypted for this installation and never returned in the review. Review and approval do not allocate another machine.</p>
      <div className="connection-actions"><button type="button" className="quiet-button" onClick={() => { setOpen(false); clearCredentials(); }}>Cancel</button><button className="primary-button" disabled={busy || !plan.sshVerified}>Review installation</button></div>
    </form>}
    {review && <form className="connection-form inline-create" aria-label="Approve target installation" onSubmit={event => void accept(event)}><h3>Install agent on {review.plan.targetName}</h3><BootstrapReview plan={review.plan} /><p>Approval expires at {new Date(review.reviewExpiresAt).toLocaleTimeString()}.</p><label>Type {review.plan.targetName} to approve<input value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label><div className="connection-actions"><button type="button" className="quiet-button" onClick={() => { setReview(null); setOpen(false); }}>Cancel</button><button className="primary-button" disabled={busy || confirmation !== review.plan.targetName}>Approve installation</button></div></form>}
    {items.filter(item => item.acceptedAt && item.state !== "cancelled").map(item => <article className="connection-form" key={item.id}><div className="section-title"><strong>{item.plan.targetName || item.serverId}</strong><StatusLabel state={item.state} /></div><div className="connection-metadata"><span>Installation <StatusLabel state={item.installationState} /></span><span>Enrollment <StatusLabel state={item.enrollmentState} /></span><span>Runtime <StatusLabel state={item.runtimeState} /></span></div><p>{item.message}</p><details><summary>Approved installation plan</summary><BootstrapReview plan={item.plan} /></details>{item.plan.method === "ssh" && ["unknown", "waiting"].includes(item.state) && <button className="quiet-button" disabled={busy || Date.parse(item.claimExpiresAt) < Date.now()} onClick={() => void act(async () => { await post(`${root}/${item.id}/retry`, { digest: item.digest }); await refresh(); })}>Retry approved installation</button>}{item.state !== "ready" && <small>Keep this machine. Recovery uses a new verified SSH review for the same target when the claim expires or its host key changes.</small>}</article>)}
    {!items.some(item => item.acceptedAt) && <p className="section-empty">Approved cloud-init and SSH installation steps appear here.</p>}
  </section>;
}
