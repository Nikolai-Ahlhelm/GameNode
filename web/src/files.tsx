import Editor, { loader } from '@monaco-editor/react';
import * as monaco from 'monaco-editor';
import { ChangeEvent, useCallback, useEffect, useMemo, useState } from 'react';
import { breadcrumbs, classifyFile, formatFileSize, isSafeRelativePath, joinRelativePath, parentRelativePath } from './files-helpers';
import { hasCapability } from './capabilities';
import { ArrowUp, Download, File, FilePlus2, Folder, FolderPlus, RefreshCw, Upload } from 'lucide-react';
import { EmptyState, LoadingState } from './ui';
import './files.css';

loader.config({ monaco });

export type FileEntry = {
  name: string;
  path: string;
  type: 'file' | 'directory';
  size: number;
  modified_at: string;
};

type FileContent = { path: string; size: number; modified_at: string; encoding: string; content: string };
type OpenFile = { entry: FileEntry; content?: FileContent; binary: boolean };

async function fileAPI<T>(path: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    headers: { ...(init?.body instanceof FormData ? {} : { 'Content-Type': 'application/json' }), ...(init?.method && init.method !== 'GET' ? { 'X-CSRF-Token': token } : {}), ...(init?.headers ?? {}) },
    ...init,
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? 'File request failed');
  }
  return response.status === 204 ? undefined as T : response.json();
}

export function FilesTab({ serverID, token, permissions }: { serverID: string; token: string; permissions?: string[] }) {
  const [currentPath, setCurrentPath] = useState('');
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [openFile, setOpenFile] = useState<OpenFile>();
  const [editorValue, setEditorValue] = useState('');
  const [savedValue, setSavedValue] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState('');
  const dirty = openFile?.content !== undefined && editorValue !== savedValue;
  const can = (permission: string) => hasCapability(permissions, permission);

  const load = useCallback(async (path = currentPath) => {
    setLoading(true);
    setError('');
    try {
      const result = await fileAPI<{ entries: FileEntry[] }>(`/servers/${serverID}/files?path=${encodeURIComponent(path)}`, token);
      setEntries(result.entries);
      setCurrentPath(path);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load directory');
    } finally {
      setLoading(false);
    }
  }, [currentPath, serverID, token]);

  useEffect(() => { void load(''); }, [serverID]);

  const requestDiscard = () => !dirty || confirm('Discard unsaved changes?');
  const navigate = (path: string) => { if (requestDiscard()) { setOpenFile(undefined); void load(path); } };

  const open = async (entry: FileEntry) => {
    if (entry.type === 'directory') return navigate(entry.path);
    if (!requestDiscard()) return;
    const classification = classifyFile(entry.name);
    if (!classification.text) { setOpenFile({ entry, binary: true }); return; }
    setLoading(true);
    setError('');
    try {
      const content = await fileAPI<FileContent>(`/servers/${serverID}/files/content?path=${encodeURIComponent(entry.path)}`, token);
      setOpenFile({ entry, content, binary: false });
      setEditorValue(content.content);
      setSavedValue(content.content);
    } catch (reason) {
      setOpenFile({ entry, binary: true });
      setError(reason instanceof Error ? reason.message : 'This file cannot be opened as text');
    } finally {
      setLoading(false);
    }
  };

  const download = (entry: FileEntry) => {
    const anchor = document.createElement('a');
    anchor.href = `/api/v1/servers/${serverID}/files/download?path=${encodeURIComponent(entry.path)}`;
    anchor.download = entry.name;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  };

  const save = async () => {
    if (!openFile?.content || !dirty) return;
    setSaving(true); setError('');
    try {
      await fileAPI<void>(`/servers/${serverID}/files/content`, token, { method: 'PUT', body: JSON.stringify({ path: openFile.entry.path, content: editorValue }) });
      setSavedValue(editorValue);
      void load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Save failed'); }
    finally { setSaving(false); }
  };

  const create = async (directory: boolean) => {
    const name = prompt(directory ? 'Folder name' : 'File name');
    if (!name || !isSafeRelativePath(name)) { if (name) setError('Use a relative name without traversal.'); return; }
    const path = joinRelativePath(currentPath, name);
    try {
      await fileAPI<void>(`/servers/${serverID}/files/${directory ? 'directory' : 'file'}`, token, { method: 'POST', body: JSON.stringify(directory ? { path } : { path, content: '' }) });
      void load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Create failed'); }
  };

  const renameOrMove = async (entry: FileEntry, rename: boolean) => {
    const value = prompt(rename ? 'New name' : 'Destination relative path', rename ? entry.name : entry.path);
    if (!value || !isSafeRelativePath(value)) { if (value) setError('Use a relative path without traversal.'); return; }
    const destination = rename ? joinRelativePath(parentRelativePath(entry.path), value) : value;
    try {
      await fileAPI<void>(`/servers/${serverID}/files/move`, token, { method: 'POST', body: JSON.stringify({ source: entry.path, destination }) });
      if (openFile?.entry.path === entry.path) setOpenFile(undefined);
      void load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Move failed'); }
  };

  const remove = async (entry: FileEntry) => {
    const recursive = entry.type === 'directory' && confirm(`Delete ${entry.name} recursively? This cannot be undone.`);
    if (!recursive && !confirm(`Delete ${entry.name}?`)) return;
    try {
      await fileAPI<void>(`/servers/${serverID}/files?path=${encodeURIComponent(entry.path)}${recursive ? '&recursive=true' : ''}`, token, { method: 'DELETE' });
      if (openFile?.entry.path === entry.path) setOpenFile(undefined);
      void load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Delete failed'); }
  };

  const upload = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) return;
    const send = async (overwrite: boolean) => {
      const body = new FormData(); body.append('file', file, file.name);
      return fileAPI<void>(`/servers/${serverID}/files/upload?path=${encodeURIComponent(currentPath)}${overwrite ? '&overwrite=true' : ''}`, token, { method: 'POST', body });
    };
    setUploading(true); setError('');
    try { await send(false); void load(); }
    catch (reason) {
      if (reason instanceof Error && reason.message.includes('conflict') && confirm(`Overwrite ${file.name}?`)) {
        try { await send(true); void load(); } catch (overwriteError) { setError(overwriteError instanceof Error ? overwriteError.message : 'Upload failed'); }
      } else setError(reason instanceof Error ? reason.message : 'Upload failed');
    } finally { setUploading(false); }
  };

  const trail = useMemo(() => breadcrumbs(currentPath), [currentPath]);
  const classification = openFile ? classifyFile(openFile.entry.name) : undefined;
  return <section className="files-panel">
    <div className="row"><div><h2>File browser</h2><p className="muted">Browse and edit files inside the configured server root.</p></div><span className="status">Server-relative paths</span></div>
    <div className="files-toolbar">
      <button className="quiet" onClick={() => navigate(parentRelativePath(currentPath))} disabled={!currentPath}><ArrowUp />Up</button>
      <button className="quiet" onClick={() => void load()} disabled={loading}><RefreshCw />Reload</button>
      {can('Files.Edit') && <><button onClick={() => void create(false)}><FilePlus2 />New file</button><button onClick={() => void create(true)}><FolderPlus />New folder</button></>}
      {can('Files.Upload') && <label className="upload-button"><Upload /><span>{uploading ? 'Uploading…' : 'Upload'}</span><input type="file" onChange={upload} disabled={uploading} /></label>}
    </div>
    <nav className="breadcrumbs">{trail.map((crumb, index) => <span key={crumb.path}>{index > 0 && ' / '}<button className="quiet" onClick={() => navigate(crumb.path)}>{crumb.label}</button></span>)}</nav>
    {error && <p className="error">{error}</p>}
    <div className="files-table" role="table"><div className="files-header" role="row"><span>Name</span><span>Type</span><span>Size</span><span>Modified</span><span>Actions</span></div>
      {loading ? <LoadingState label="Loading directory…" /> : entries.length === 0 ? <EmptyState compact title="This directory is empty" description="Create a file or folder, or upload content into this directory." icon={Folder} /> : entries.map(entry => <div className="files-row" role="row" key={entry.path}><button className="file-name" onClick={() => void open(entry)}>{entry.type === 'directory' ? <Folder /> : <File />}{entry.name}</button><span>{entry.type}</span><span>{entry.type === 'file' ? formatFileSize(entry.size) : '—'}</span><span>{new Date(entry.modified_at).toLocaleString()}</span><span className="file-actions">{entry.type === 'file' && can('Files.Download') && <button className="quiet" aria-label={`Download ${entry.name}`} onClick={() => download(entry)}><Download />Download</button>}{can('Files.Rename') && <><button className="quiet" onClick={() => void renameOrMove(entry, true)}>Rename</button><button className="quiet" onClick={() => void renameOrMove(entry, false)}>Move</button></>}{can('Files.Delete') && <button className="danger quiet" onClick={() => void remove(entry)}>Delete</button>}</span></div>)}</div>
    {openFile && <section className="file-editor panel"><div className="row"><div><h3>{openFile.entry.path}</h3><p className="muted">{formatFileSize(openFile.entry.size)} · {openFile.binary ? 'Binary or unsupported file' : classification?.readOnly || !can('Files.Edit') ? 'Read-only' : dirty ? 'Unsaved changes' : 'Saved'}</p></div><div className="actions">{can('Files.Download') && <button className="quiet" onClick={() => download(openFile.entry)}>Download</button>}<button className="quiet" onClick={() => { if (requestDiscard()) void open(openFile.entry); }}>Reload</button>{!openFile.binary && !classification?.readOnly && can('Files.Edit') && <button onClick={() => void save()} disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save'}</button>}</div></div>{openFile.binary ? <p className="muted">This file is not rendered as text. Download it to inspect its contents.</p> : <Editor height="480px" language={classification?.language} value={editorValue} onChange={value => setEditorValue(value ?? '')} options={{ readOnly: classification?.readOnly || !can('Files.Edit'), minimap: { enabled: false }, wordWrap: 'on', automaticLayout: true }} />}</section>}
  </section>;
}
