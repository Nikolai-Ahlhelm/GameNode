import { ChangeEvent, useCallback, useEffect, useMemo, useState } from 'react';
import { CircleAlert, Package, Power, PowerOff, RefreshCw, Trash2, Upload } from 'lucide-react';
import { EmptyState, SectionHeader, SkeletonRows } from './ui';
import {
  defaultLoaderVersion, filterMods, formatModSize, javaAdvice, loaderDescription, loaderLabel, loaderNeedsVersion, minecraftLoaders, minecraftSelectionError, minecraftVersionsPath,
  loadedModMismatches, modCompatibility, modCountLabel, modDisplayName, modEnabled, modsDeletePath, modsStatePath, modsUploadPath, validModFileName, defaultModsLayout, modsAcceptAttribute, modsExtensionText,
  type ModsLayout,
  type JavaInfo, type LoaderVersion, type MinecraftMod,
} from './minecraft-helpers';
import './files.css';
import './minecraft.css';

async function minecraftAPI<T>(path: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: { ...(init?.headers ?? {}), ...(init?.method && init.method !== 'GET' ? { 'X-CSRF-Token': token } : {}) },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? 'Request failed');
  }
  return response.status === 204 ? undefined as T : response.json();
}

type Values = Record<string, string>;
type VersionsResponse = { loader: string; game_versions: string[]; loader_versions?: LoaderVersion[]; java?: JavaInfo };

/**
 * Loader and exact version selection for the Minecraft Java template. Options
 * come only from GameNode's fixed upstream sources via /minecraft/versions; the
 * user can pick, never type, a version.
 */
export function MinecraftVersionPicker({ values, setValues }: { values: Values; setValues: (update: (previous: Values) => Values) => void }) {
  const loader = values.LOADER || 'vanilla';
  const minecraftVersion = values.MINECRAFT_VERSION ?? '';
  const [games, setGames] = useState<string[]>([]);
  const [builds, setBuilds] = useState<LoaderVersion[]>([]);
  const [java, setJava] = useState<JavaInfo>();
  const [loadingGames, setLoadingGames] = useState(true);
  const [loadingBuilds, setLoadingBuilds] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoadingGames(true); setError(''); setGames([]);
    minecraftAPI<VersionsResponse>(minecraftVersionsPath(loader), '')
      .then(result => {
        if (cancelled) return;
        setGames(result.game_versions ?? []);
        setValues(previous => (result.game_versions ?? []).includes(previous.MINECRAFT_VERSION ?? '') ? previous : { ...previous, MINECRAFT_VERSION: result.game_versions?.[0] ?? '', LOADER_VERSION: '' });
      })
      .catch(reason => { if (!cancelled) setError(reason instanceof Error ? reason.message : 'Versions could not be loaded'); })
      .finally(() => { if (!cancelled) setLoadingGames(false); });
    return () => { cancelled = true; };
  }, [loader, attempt]);

  useEffect(() => {
    if (!minecraftVersion || loadingGames || !games.includes(minecraftVersion)) { setBuilds([]); return; }
    let cancelled = false;
    setLoadingBuilds(true); setError('');
    minecraftAPI<VersionsResponse>(minecraftVersionsPath(loader, minecraftVersion), '')
      .then(result => {
        if (cancelled) return;
        const list = result.loader_versions ?? [];
        setBuilds(list); setJava(result.java);
        setValues(previous => !loaderNeedsVersion(loader) ? { ...previous, LOADER_VERSION: '' } : list.some(build => build.version === previous.LOADER_VERSION) ? previous : { ...previous, LOADER_VERSION: defaultLoaderVersion(list) });
      })
      .catch(reason => { if (!cancelled) setError(reason instanceof Error ? reason.message : 'Versions could not be loaded'); })
      .finally(() => { if (!cancelled) setLoadingBuilds(false); });
    return () => { cancelled = true; };
  }, [loader, minecraftVersion, loadingGames, games]);

  const advice = javaAdvice(java);
  const problem = minecraftSelectionError(loader, minecraftVersion, values.LOADER_VERSION ?? '');
  return <div className="definition-list minecraft-picker">
    <strong>Minecraft</strong>
    <fieldset className="minecraft-loaders" aria-label="Server type">
      {minecraftLoaders.map(option => <label key={option} className={loader === option ? 'selected' : ''}>
        <input type="radio" name="minecraft-loader" checked={loader === option} onChange={() => setValues(previous => ({ ...previous, LOADER: option, LOADER_VERSION: '' }))} />
        <span>{loaderLabel(option)}</span><small>{loaderDescription(option)}</small>
      </label>)}
    </fieldset>
    <label>Minecraft version *
      <select value={minecraftVersion} disabled={loadingGames || games.length === 0} onChange={event => setValues(previous => ({ ...previous, MINECRAFT_VERSION: event.target.value, LOADER_VERSION: '' }))}>
        {loadingGames && <option value="">Loading versions…</option>}
        {!loadingGames && games.length === 0 && <option value="">No versions available</option>}
        {games.map(version => <option key={version} value={version}>{version}</option>)}
      </select>
      <small>Release versions offered for {loaderLabel(loader)} by its official source.</small>
    </label>
    {loaderNeedsVersion(loader) && <label>{loaderLabel(loader)} version *
      <select value={values.LOADER_VERSION ?? ''} disabled={loadingBuilds || builds.length === 0} onChange={event => setValues(previous => ({ ...previous, LOADER_VERSION: event.target.value }))}>
        {loadingBuilds && <option value="">Loading builds…</option>}
        {!loadingBuilds && builds.length === 0 && <option value="">No builds for this Minecraft version</option>}
        {builds.map(build => <option key={build.version} value={build.version}>{build.version}{build.latest ? ' (latest stable)' : ''}{!build.stable ? ' (beta)' : ''}</option>)}
      </select>
      <small>The exact build GameNode installs; it is never upgraded automatically.</small>
    </label>}
    {error && <p className="error notice" role="alert">{error} <button type="button" className="quiet" onClick={() => setAttempt(value => value + 1)}><RefreshCw />Retry</button></p>}
    {advice && <p className={`notice ${advice.tone === 'ok' ? '' : 'notice--warning'}`}>{advice.text}</p>}
    {!loadingGames && !loadingBuilds && problem && !error && <small className="error">{problem}</small>}
  </div>;
}

export function MinecraftModsTab({ serverID, token, canUpload, canDelete, canToggle, running }: { serverID: string; token: string; canUpload: boolean; canDelete: boolean; canToggle: boolean; running: boolean }) {
  const [mods, setMods] = useState<MinecraftMod[]>();
  const [loader, setLoader] = useState<string>();
  const [layout, setLayout] = useState<ModsLayout>(defaultModsLayout);
  const [maxUpload, setMaxUpload] = useState(0);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(() => minecraftAPI<{ available: boolean; game?: string; directory?: string; extensions?: string[]; loader?: string; mods: MinecraftMod[]; max_upload_bytes?: number }>(`/servers/${serverID}/mods`, token)
    .then(result => { setMods(result.mods ?? []); setLoader(result.loader || undefined); setLayout(result.game && result.directory && result.extensions?.length ? { game: result.game, directory: result.directory, extensions: result.extensions } : defaultModsLayout); setMaxUpload(result.max_upload_bytes ?? 0); })
    .catch(reason => { setMods(current => current ?? []); setError(reason instanceof Error ? reason.message : 'Mods could not be loaded'); }), [serverID, token]);
  useEffect(() => { setMods(undefined); void load(); }, [load]);

  async function upload(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? []);
    event.target.value = '';
    if (!files.length) return;
    setBusy('upload'); setError(''); setNotice('');
    const added: string[] = [];
    try {
      for (const file of files) {
        if (!validModFileName(file.name, layout.extensions)) throw new Error(`${file.name} is not a valid mod file name; only ${modsExtensionText(layout.extensions)} files are accepted.`);
        if (maxUpload && file.size > maxUpload) throw new Error(`${file.name} exceeds the ${formatModSize(maxUpload)} upload limit.`);
        const send = (overwrite: boolean) => { const body = new FormData(); body.append('file', file, file.name); return minecraftAPI<MinecraftMod>(modsUploadPath(serverID, overwrite), token, { method: 'POST', body }); };
        try { await send(false); }
        catch (reason) {
          if (reason instanceof Error && reason.message.includes('conflict') && confirm(`${file.name} already exists. Replace it?`)) await send(true);
          else throw reason;
        }
        added.push(file.name);
      }
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Upload failed'); }
    finally {
      setBusy('');
      if (added.length) setNotice(`Added ${added.length === 1 ? added[0] : `${added.length} mods`}. Restart the server to load ${added.length === 1 ? 'it' : 'them'}.`);
      void load();
    }
  }

  async function toggle(mod: MinecraftMod) {
    const enable = !modEnabled(mod);
    setBusy(mod.file_name); setError(''); setNotice('');
    try {
      await minecraftAPI<MinecraftMod>(modsStatePath(serverID), token, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ file: mod.file_name, enabled: enable }) });
      setNotice(`${modDisplayName(mod)} ${enable ? 'enabled' : 'disabled'}. Restart the server to apply the change.`);
      await load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'The mod could not be changed'); }
    finally { setBusy(''); }
  }

  async function remove(mod: MinecraftMod) {
    if (!confirm(`Remove ${modDisplayName(mod)} (${mod.file_name}) from the server? This deletes the file.`)) return;
    setBusy(mod.file_name); setError(''); setNotice('');
    try { await minecraftAPI<void>(modsDeletePath(serverID, mod.file_name), token, { method: 'DELETE' }); setNotice(`Removed ${mod.file_name}. Restart the server to unload it.`); await load(); }
    catch (reason) { setError(reason instanceof Error ? reason.message : 'Removal failed'); }
    finally { setBusy(''); }
  }

  const visible = useMemo(() => filterMods(mods ?? [], query), [mods, query]);
  const mismatches = loadedModMismatches(loader, mods ?? []);
  return <section className="files-panel mods-panel">
    <SectionHeader title="Mods" description={`${modsExtensionText(layout.extensions)} files in ${layout.directory}${loader ? ` · ${loaderLabel(loader)} server` : ''}`} actions={<button className="quiet" onClick={() => { setError(''); setNotice(''); void load(); }}><RefreshCw />Refresh</button>} />
    {running && <p className="notice notice--warning">The server is running. Mod changes take effect after a restart, and some files may be locked until it stops.</p>}
    {error && <p className="error notice" role="alert">{error}</p>}
    {notice && <p className="notice" role="status">{notice}</p>}
    {mismatches > 0 && <p className="notice notice--warning"><CircleAlert /> {mismatches} mod{mismatches === 1 ? '' : 's'} declare{mismatches === 1 ? 's' : ''} no support for this server's {loaderLabel(loader ?? '')} loader and will likely be ignored or fail to load.</p>}
    <div className="files-toolbar">
      {canUpload && <label className="upload-button"><Upload /><span>{busy === 'upload' ? 'Uploading…' : 'Add mods'}</span><input type="file" accept={modsAcceptAttribute(layout.extensions)} multiple onChange={upload} disabled={busy !== ''} /></label>}
      <input className="mods-search" type="search" placeholder="Filter by name, id or version" aria-label="Filter mods" value={query} onChange={event => setQuery(event.target.value)} />
      <span className="muted mods-count">{mods ? modCountLabel(mods) : ''}</span>
    </div>
    <div className="files-table mods-table">
      {mods === undefined ? <SkeletonRows count={4} label="Loading mods…" />
        : mods.length === 0 ? <EmptyState compact icon={Package} title="No mods installed" description={canUpload ? `Add ${modsExtensionText(layout.extensions)} mod files that match this server's game version${layout.game === 'minecraft' ? ' and loader' : ''}.` : `No mod files are present in ${layout.directory}.`} />
        : visible.length === 0 ? <EmptyState compact icon={Package} title="No matching mods" description="Change the filter to see the installed mods." />
        : <>
          <div className="files-header mods-row" role="row"><span>Mod</span><span>Version</span><span>{layout.game === 'minecraft' ? 'Loader' : 'Type'}</span><span>Size</span><span>Actions</span></div>
          {visible.map(mod => {
            const compatibility = modEnabled(mod) ? modCompatibility(loader, mod) : 'unknown';
            return <div className={`files-row mods-row${modEnabled(mod) ? '' : ' mod-disabled'}`} role="row" key={mod.file_name}>
              <span className="mod-name"><strong>{modDisplayName(mod)}{!modEnabled(mod) && <span className="status mod-badge">Disabled</span>}</strong><small title={mod.description}>{mod.file_name}</small></span>
              <span>{mod.version || '—'}</span>
              <span>{mod.loaders?.length ? mod.loaders.map(loaderLabel).join(', ') : layout.game === 'minecraft' ? 'Unknown' : '—'}{compatibility === 'mismatch' && <small className="error"> · not for {loaderLabel(loader ?? '')}</small>}</span>
              <span>{formatModSize(mod.size)}</span>
              <span className="file-actions">{canToggle && <button className="quiet" disabled={busy !== ''} onClick={() => void toggle(mod)} aria-label={`${modEnabled(mod) ? 'Disable' : 'Enable'} ${mod.file_name}`}>{modEnabled(mod) ? <PowerOff /> : <Power />}{busy === mod.file_name ? 'Working…' : modEnabled(mod) ? 'Disable' : 'Enable'}</button>}{canDelete && <button className="quiet danger" disabled={busy !== ''} onClick={() => void remove(mod)} aria-label={`Remove ${mod.file_name}`}><Trash2 />Remove</button>}</span>
            </div>;
          })}
        </>}
    </div>
    <p className="muted mods-footnote">Disabled mods stay on disk with a .disabled suffix and are not loaded. Mods are added by upload only; GameNode never downloads a mod from a URL and does not check mods against the game version.</p>
  </section>;
}
