import { useEffect, useState } from 'react';
import { RefreshCw } from 'lucide-react';
import {
  defaultVersion, dotnetAdvice, vintageStorySelectionError, vintageStoryVersionsPath, versionOptionLabel,
  type DotnetInfo, type VintageStoryVersion,
} from './vintagestory-helpers';

type Values = Record<string, string>;

/**
 * Exact Vintage Story version selection for the provisioning wizard. Options come
 * only from GameNode's fixed official source via /vintagestory/versions; the user
 * can pick, never type, a version.
 */
export function VintageStoryVersionPicker({ values, setValues }: { values: Values; setValues: (update: (previous: Values) => Values) => void }) {
  const version = values.VS_VERSION ?? '';
  const [versions, setVersions] = useState<VintageStoryVersion[]>([]);
  const [dotnet, setDotnet] = useState<DotnetInfo>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [showRC, setShowRC] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true); setError('');
    fetch(`/api/v1${vintageStoryVersionsPath()}`, { credentials: 'same-origin' })
      .then(async response => {
        if (!response.ok) throw new Error((await response.json().catch(() => null))?.error?.message ?? 'Versions could not be loaded');
        return response.json() as Promise<{ versions: VintageStoryVersion[]; dotnet?: DotnetInfo }>;
      })
      .then(result => {
        if (cancelled) return;
        const list = result.versions ?? [];
        setVersions(list); setDotnet(result.dotnet);
        setValues(previous => list.some(item => item.version === previous.VS_VERSION) ? previous : { ...previous, VS_VERSION: defaultVersion(list) });
      })
      .catch(reason => { if (!cancelled) setError(reason instanceof Error ? reason.message : 'Versions could not be loaded'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [attempt]);

  // A release candidate is shown when the operator asks for them or already selected one.
  const selectedIsRC = versions.find(item => item.version === version)?.channel === 'rc';
  const visible = versions.filter(item => item.channel === 'stable' || showRC || selectedIsRC);
  const advice = dotnetAdvice(dotnet, version);
  const problem = vintageStorySelectionError(version, versions);
  return <div className="definition-list vintagestory-picker">
    <strong>Vintage Story</strong>
    <label>Version *
      <select value={version} disabled={loading || visible.length === 0} onChange={event => setValues(previous => ({ ...previous, VS_VERSION: event.target.value }))}>
        {loading && <option value="">Loading versions…</option>}
        {!loading && visible.length === 0 && <option value="">No versions available</option>}
        {visible.map(item => <option key={item.version} value={item.version}>{versionOptionLabel(item)}</option>)}
      </select>
      <small>Versions 1.21.0 and newer, offered by the official source. The installed version is never upgraded automatically.</small>
    </label>
    <label className="checkbox"><input type="checkbox" checked={showRC || selectedIsRC} disabled={selectedIsRC} onChange={event => setShowRC(event.target.checked)} />Show release candidates (pre-release builds)</label>
    {error && <p className="error notice" role="alert">{error} <button type="button" className="quiet" onClick={() => setAttempt(value => value + 1)}><RefreshCw />Retry</button></p>}
    {advice && <p className={`notice ${advice.tone === 'ok' ? '' : 'notice--warning'}`}>{advice.text}</p>}
    {!loading && problem && !error && <small className="error">{problem}</small>}
  </div>;
}
