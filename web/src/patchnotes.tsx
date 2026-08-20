import { useEffect, useState, type ReactNode } from 'react';
import './patchnotes.css';
import './patchnotes-overrides.css';

const README_URL = 'https://raw.githubusercontent.com/Nikolai-Ahlhelm/GameNode/main/README.md';

type Version = { major: number; minor: number; patch: number };
type ReleaseNote = { version: string; lines: string[] };
type DiagnosticsResponse = { application?: { version?: string } };

function parseVersion(value: string): Version | undefined {
  const match = value.trim().replace(/^v/i, '').match(/^(\d+)\.(\d+)\.(\d+)/);
  return match ? { major: Number(match[1]), minor: Number(match[2]), patch: Number(match[3]) } : undefined;
}

function isAtMost(version: string, installed: string): boolean {
  const current = parseVersion(installed);
  const release = parseVersion(version);
  if (!current || !release) return true;
  if (release.major !== current.major) return release.major < current.major;
  if (release.minor !== current.minor) return release.minor < current.minor;
  return release.patch <= current.patch;
}

function parseReleaseNotes(markdown: string): ReleaseNote[] {
  const history = markdown.match(/^## Version history[\s\S]*$/m)?.[0] ?? '';
  const headings = [...history.matchAll(/^### (v?\d+\.\d+\.\d+(?:[-+][\w.-]+)?)\s*$/gm)];
  return headings.map((heading, index) => ({
    version: heading[1],
    lines: history.slice(heading.index! + heading[0].length, headings[index + 1]?.index ?? history.length).trim().split('\n'),
  }));
}

function cleanMarkdown(value: string): string {
  return value.replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/`([^`]+)`/g, '$1').trim();
}

function ReleaseBody({ lines }: { lines: string[] }) {
  const blocks: ReactNode[] = [];
  let bullets: string[] = [];
  const flush = () => { if (bullets.length) { blocks.push(<ul key={`list-${blocks.length}`}>{bullets.map((item, index) => <li key={`${index}-${item}`}>{cleanMarkdown(item)}</li>)}</ul>); bullets = []; } };
  lines.forEach((line, index) => {
    const value = line.trim();
    if (!value) { flush(); return; }
    if (value.startsWith('- ')) { bullets.push(value.slice(2)); return; }
    flush();
    if (value.startsWith('##### ')) blocks.push(<h4 key={`heading-${index}`}>{cleanMarkdown(value.slice(6))}</h4>);
    else if (value.startsWith('#### ')) blocks.push(<h3 key={`heading-${index}`}>{cleanMarkdown(value.slice(5))}</h3>);
    else blocks.push(<p key={`paragraph-${index}`}>{cleanMarkdown(value)}</p>);
  });
  flush();
  return <>{blocks}</>;
}

export function PatchNotesPage() {
  const [notes, setNotes] = useState<ReleaseNote[]>([]);
  const [installedVersion, setInstalledVersion] = useState('dev');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      fetch(README_URL, { signal: controller.signal }).then(response => { if (!response.ok) throw new Error('README unavailable'); return response.text(); }),
      fetch('/api/v1/diagnostics', { credentials: 'same-origin', signal: controller.signal }).then(response => response.ok ? response.json() as Promise<DiagnosticsResponse> : ({}) as DiagnosticsResponse),
    ]).then(([readme, diagnostics]) => {
      const version = diagnostics.application?.version || 'dev';
      setInstalledVersion(version);
      setNotes(parseReleaseNotes(readme).filter(note => isAtMost(note.version, version)));
    }).catch(reason => { if (reason?.name !== 'AbortError') setError('Patch notes could not be loaded. Check the connection to GitHub.'); }).finally(() => setLoading(false));
    return () => controller.abort();
  }, []);

  return <section className="patchnotes-page"><div className="page-header"><div><p className="eyebrow">GameNode</p><h1>Patch notes</h1><p>Release highlights from the GameNode repository, filtered to version {installedVersion}.</p></div><a className="quiet patchnotes-source" href="https://github.com/Nikolai-Ahlhelm/GameNode/blob/main/README.md#version-history" target="_blank" rel="noreferrer">View source</a></div>{loading ? <section className="panel"><p className="muted">Loading patch notes…</p></section> : error ? <section className="panel"><p className="error">{error}</p><a href={README_URL} target="_blank" rel="noreferrer">Open README on GitHub</a></section> : notes.length === 0 ? <section className="panel"><p className="muted">No release notes for this installed version were found.</p></section> : <div className="patchnotes-list">{notes.map(note => <article className="panel patchnote" key={note.version}><div className="patchnote-header"><h2>{note.version}</h2><span className="status running">Included</span></div><ReleaseBody lines={note.lines} /></article>)}</div>}</section>;
}
