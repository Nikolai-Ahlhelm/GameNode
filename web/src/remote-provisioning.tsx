import { FormEvent, useEffect, useMemo, useState } from 'react';
import { CheckCircle2, HardDriveDownload, LoaderCircle, XCircle } from 'lucide-react';
import { SectionHeader } from './ui';
import { editableTemplateValues, provisioningStatusLabel, provisioningTerminal, safeDirectoryName, templateInputType, validateTemplateValue } from './templates-helpers';
import type { ProvisionTemplate } from './template-provision';
import { resolveTenantSelection, type TenantOption } from './tenants-helpers';
import './templates.css';

type Job = { id: string; template_name: string; server_name: string; directory_name: string; runtime_type?: string; selected_image?: string; status: string; summary: string; error_summary?: string; files_may_remain: boolean; server_id?: string };
type Selected = { kind: 'local' } | { kind: 'remote'; nodeID: string };

export type DeployTarget = { kind: 'node'; nodeID: string; nodeCapabilities: string[] } | { kind: 'cluster' };

async function api<T>(path: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.method && init.method !== 'GET' ? { 'X-CSRF-Token': token } : {}), ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error?.message ?? 'Deployment request failed.'); }
  return response.status === 204 ? undefined as T : response.json();
}

function jobPath(selected: Selected, jobID: string): string {
  return selected.kind === 'local' ? `/provisioning/jobs/${jobID}` : `/remote-nodes/${encodeURIComponent(selected.nodeID)}/provisioning/${jobID}`;
}

// RemoteTemplateDeploy deploys a Game Library template either to one
// explicitly chosen enrolled remote node (target.kind==='node', via
// POST /remote-nodes/{nodeID}/provisioning) or lets the v0.6 cluster
// placement engine pick the best eligible node - local or remote
// (target.kind==='cluster', via POST /cluster/placement/execute). Both
// routes already validate the request through the target node's own
// provisioning.Service; this component never precomputes or claims
// compatibility for a node other than the one running this browser session
// - the target node's own typed error (e.g. container_image_not_declared,
// not_provisionable) is surfaced verbatim instead of a fabricated precheck.
export function RemoteTemplateDeploy({ token, tenants, target, onCancel, onDeployed }: { token: string; tenants?: TenantOption[]; target: DeployTarget; onCancel: () => void; onDeployed?: () => void }) {
  const selection = resolveTenantSelection(tenants ?? []);
  const [templates, setTemplates] = useState<ProvisionTemplate[]>([]);
  const [templateID, setTemplateID] = useState('');
  const [loadError, setLoadError] = useState('');
  const [tenantID, setTenantID] = useState(selection.preselected);
  const [name, setName] = useState('');
  const [directory, setDirectory] = useState('');
  const [values, setValues] = useState<Record<string, string>>({});
  const [runtimeType, setRuntimeType] = useState<'native' | 'container'>('native');
  const [selectedImage, setSelectedImage] = useState('');
  const [memory, setMemory] = useState('1073741824');
  const [cpu, setCPU] = useState('1000');
  const [job, setJob] = useState<Job>();
  const [selected, setSelected] = useState<Selected>({ kind: 'local' });
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api<{ templates: ProvisionTemplate[] }>('/templates', token)
      .then(result => setTemplates(result.templates.filter(item => !!item.id)))
      .catch(reason => setLoadError(reason instanceof Error ? reason.message : 'Templates could not be loaded'));
  }, [token]);

  const template = templates.find(item => item.id === templateID);

  useEffect(() => {
    if (!template) return;
    setName(current => current || template.name);
    setDirectory(current => current || safeDirectoryName(template.name));
    setValues(Object.fromEntries((template.variables ?? []).map(v => [v.key, v.default_value])));
    const images = template.container_runtime?.images ?? [];
    setSelectedImage(images[0] ?? '');
    setRuntimeType('native');
    setMemory(String(template.container_runtime?.resource_defaults?.memory_limit_bytes ?? 1073741824));
    setCPU(String(template.container_runtime?.resource_defaults?.cpu_limit_millis ?? 1000));
  }, [template?.id]);

  useEffect(() => {
    if (!job || provisioningTerminal(job.status)) return;
    const timer = window.setInterval(() => api<Job>(jobPath(selected, job.id), token).then(setJob).catch(reason => setError(reason instanceof Error ? reason.message : 'Deployment status could not be refreshed')), 1000);
    return () => window.clearInterval(timer);
  }, [job?.id, job?.status, selected, token]);

  useEffect(() => { if (job?.status === 'completed') onDeployed?.(); }, [job?.status]);

  const editable = useMemo(() => (template?.variables ?? []).filter(variable => variable.user_editable), [template]);
  const validation = Object.fromEntries(editable.map(variable => [variable.key, validateTemplateValue(variable, values[variable.key] ?? '')]).filter(([, message]) => message));
  const containerAvailable = (template?.container_runtime?.images.length ?? 0) > 0;

  async function deploy(event?: FormEvent, recoverExisting?: unknown) {
    event?.preventDefault();
    if (!template || Object.keys(validation).length) return;
    setBusy(true); setError('');
    const body = {
      template_id: template.id, server_name: name, directory_name: directory, tenant_id: tenantID,
      variables: editableTemplateValues(template.variables, values), recover_existing: recoverExisting === true,
      runtime_type: runtimeType, image: runtimeType === 'container' ? selectedImage : undefined,
      memory_limit_bytes: runtimeType === 'container' ? Number(memory) : undefined,
      cpu_limit_millis: runtimeType === 'container' ? Number(cpu) : undefined,
    };
    try {
      if (target.kind === 'node') {
        setSelected({ kind: 'remote', nodeID: target.nodeID });
        setJob(await api<Job>(`/remote-nodes/${encodeURIComponent(target.nodeID)}/provisioning`, token, { method: 'POST', body: JSON.stringify(body) }));
      } else {
        const result = await api<{ decision: { selected?: { kind: string; node_id: string } }; job: Job }>('/cluster/placement/execute', token, { method: 'POST', body: JSON.stringify(body) });
        setSelected(result.decision.selected?.kind === 'remote' ? { kind: 'remote', nodeID: result.decision.selected.node_id } : { kind: 'local' });
        setJob(result.job);
      }
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Deployment could not start'); } finally { setBusy(false); }
  }

  async function cancel() {
    if (!job) return;
    try { setJob(await api<Job>(`${jobPath(selected, job.id)}/cancel`, token, { method: 'POST' })); }
    catch (reason) { setError(reason instanceof Error ? reason.message : 'Cancellation failed'); }
  }

  if (job) {
    const terminal = provisioningTerminal(job.status);
    return <section className="panel provision-review">
      <SectionHeader title="Deployment" description={target.kind === 'node' ? 'Deploying directly to the chosen remote node.' : selected.kind === 'remote' ? 'The cluster placement engine selected a remote node.' : 'The cluster placement engine selected this node.'} />
      {error && <p className="error notice">{error}</p>}
      <div className={`panel provision-progress provision-progress--${job.status}`}>
        {job.status === 'completed' ? <CheckCircle2 /> : job.status === 'failed' || job.status === 'cancelled' ? <XCircle /> : <LoaderCircle className="spin" />}
        <p className="eyebrow">{provisioningStatusLabel(job.status)}</p>
        <h3>{job.summary}</h3>
        {job.error_summary && <p className="error">{job.error_summary}</p>}
        {job.files_may_remain && <p className="notice notice--warning">Files may remain on the target node. No server record is created there unless completion succeeded.</p>}
        <div className="actions">
          {!terminal && <button className="danger quiet" onClick={cancel}>Cancel deployment</button>}
          {job.status === 'failed' && job.files_may_remain && <button disabled={busy} onClick={() => void deploy(undefined, true)}>Register installed server</button>}
          {terminal && <button className="quiet" onClick={onCancel}>Close</button>}
        </div>
      </div>
    </section>;
  }

  return <form className="panel form-panel provision-form" onSubmit={event => void deploy(event)}>
    <SectionHeader title={target.kind === 'node' ? 'Deploy template to this node' : 'Deploy template · cluster placement'} description="The target node validates compatibility, images, and resource limits independently when the request is submitted - nothing here is prechecked against that node." />
    {loadError && <p className="error notice">{loadError}</p>}
    {error && <p className="error notice">{error}</p>}
    <label>Template
      <select value={templateID} required onChange={event => setTemplateID(event.target.value)}>
        <option value="">Choose a template…</option>
        {templates.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select>
    </label>
    {template && <>
      {selection.locked ? <label>Tenant<input value={tenants?.find(t => t.id === tenantID)?.name ?? tenantID} readOnly disabled /></label> : <label>Tenant<select value={tenantID} required onChange={event => setTenantID(event.target.value)}><option value="">Choose tenant…</option>{(tenants ?? []).map(t => <option key={t.id} value={t.id}>{t.name}</option>)}</select></label>}
      <div className="form-grid">
        <label>Server name<input value={name} maxLength={100} required onChange={event => setName(event.target.value)} /></label>
        <label>Managed folder name<input value={directory} pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" required onChange={event => setDirectory(event.target.value)} /></label>
      </div>
      {containerAvailable && <div className="runtime-choice">
        <label><input type="radio" name="remote-deploy-runtime" checked={runtimeType === 'native'} onChange={() => setRuntimeType('native')} /> Native</label>
        <label><input type="radio" name="remote-deploy-runtime" checked={runtimeType === 'container'} onChange={() => setRuntimeType('container')} /> Container</label>
      </div>}
      {runtimeType === 'container' && containerAvailable && <div className="container-provision-options">
        <label>Game image<select value={selectedImage} onChange={event => setSelectedImage(event.target.value)}>{(template.container_runtime?.images ?? []).map(image => <option key={image} value={image}>{image}</option>)}</select></label>
        <div className="form-grid">
          <label>Memory limit (bytes)<input type="number" min="16777216" value={memory} onChange={event => setMemory(event.target.value)} /></label>
          <label>CPU limit (millicores)<input type="number" min="10" value={cpu} onChange={event => setCPU(event.target.value)} /></label>
        </div>
      </div>}
      {editable.length > 0 && <div className="definition-list">
        <strong>Configuration</strong>
        {editable.map(variable => <label key={variable.key}>{variable.name}{variable.required && ' *'}
          {variable.validation.allowed?.length
            ? <select value={values[variable.key] ?? ''} onChange={event => setValues({ ...values, [variable.key]: event.target.value })}>{variable.validation.allowed.map(value => <option key={value}>{value}</option>)}</select>
            : templateInputType(variable.type, variable.sensitive) === 'checkbox'
              ? <input type="checkbox" checked={values[variable.key] === '1' || values[variable.key] === 'true'} onChange={event => setValues({ ...values, [variable.key]: event.target.checked ? '1' : '0' })} />
              : <input type={templateInputType(variable.type, variable.sensitive)} value={values[variable.key] ?? ''} placeholder={variable.placeholder} min={variable.validation.min} max={variable.validation.max} minLength={variable.validation.min_length} maxLength={variable.validation.max_length} required={variable.required && !variable.nullable} onChange={event => setValues({ ...values, [variable.key]: event.target.value })} />}
          <small>{variable.description}{validation[variable.key] && <span className="error"> · {validation[variable.key]}</span>}</small>
        </label>)}
      </div>}
    </>}
    <div className="actions">
      <button type="button" className="quiet" onClick={onCancel}>Cancel</button>
      <button disabled={!template || busy || !!Object.keys(validation).length || !name.trim() || !directory.trim() || !tenantID}><HardDriveDownload />{busy ? 'Starting…' : 'Deploy'}</button>
    </div>
  </form>;
}
