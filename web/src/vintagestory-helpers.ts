export type VintageStoryVersion = { version: string; channel: 'stable' | 'rc'; latest?: boolean };

export type DotnetInfo = { found: boolean; installed_majors: number[]; required_major: Record<string, number> };

export function channelLabel(channel: string): string {
  return channel === 'rc' ? 'release candidate' : 'stable';
}

export function vintageStoryVersionsPath(): string {
  return '/vintagestory/versions';
}

/** Label for one picker option, e.g. "1.22.7 (latest stable)" or "1.22.0-rc.10 (release candidate)". */
export function versionOptionLabel(version: VintageStoryVersion): string {
  if (version.latest) return `${version.version} (latest stable)`;
  return version.channel === 'rc' ? `${version.version} (release candidate)` : version.version;
}

/** The newest stable version, which is the preferred default. */
export function defaultVersion(versions: VintageStoryVersion[]): string {
  return versions.find(version => version.latest)?.version ?? versions.find(version => version.channel === 'stable')?.version ?? versions[0]?.version ?? '';
}

export function vintageStorySelectionError(version: string, offered: VintageStoryVersion[]): string | undefined {
  if (!/^\d{1,2}\.\d{1,2}\.\d{1,3}(-rc\.\d{1,3})?$/.test(version)) return 'Choose a Vintage Story version.';
  if (offered.length > 0 && !offered.some(item => item.version === version)) return 'This version is not offered by the official source.';
  return undefined;
}

/** Advisory warning about the .NET runtime a version needs; the installer makes the authoritative check. */
export function dotnetAdvice(info: DotnetInfo | undefined, version: string): { tone: 'ok' | 'warning' | 'danger'; text: string } | undefined {
  const required = info?.required_major?.[version];
  if (!info || !required) return undefined;
  if (!info.found) return { tone: 'danger', text: `.NET was not found on this host. Version ${version} needs the .NET ${required} runtime (DOTNET_ROOT or PATH).` };
  if (!info.installed_majors.includes(required)) {
    const installed = [...info.installed_majors].sort((left, right) => left - right).join(', ');
    return { tone: 'warning', text: `Version ${version} needs the .NET ${required} runtime; this host has ${installed ? `.NET ${installed}` : 'no .NET runtime'}. The installation will stop with an error until it is installed.` };
  }
  return { tone: 'ok', text: `.NET ${required} runtime detected.` };
}
