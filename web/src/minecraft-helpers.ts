export type MinecraftLoader = 'vanilla' | 'neoforge' | 'forge' | 'fabric';

export const minecraftLoaders: MinecraftLoader[] = ['vanilla', 'neoforge', 'forge', 'fabric'];

export type LoaderVersion = { version: string; stable: boolean; latest?: boolean };

export type MinecraftMod = {
  file_name: string;
  size: number;
  modified_at: string;
  id?: string;
  name?: string;
  version?: string;
  description?: string;
  loaders?: string[];
  disabled?: boolean;
};

export function loaderLabel(loader: string): string {
  return ({ vanilla: 'Vanilla', neoforge: 'NeoForge', forge: 'Forge', fabric: 'Fabric', quilt: 'Quilt' } as Record<string, string>)[loader] ?? loader;
}

export function loaderDescription(loader: string): string {
  return ({
    vanilla: 'Mojang\'s unmodified server. No mods.',
    neoforge: 'NeoForge mod loader (Minecraft 1.20.2 and newer).',
    forge: 'Minecraft Forge mod loader (Minecraft 1.17 and newer).',
    fabric: 'Fabric mod loader, lightweight and fast to update.',
  } as Record<string, string>)[loader] ?? '';
}

export function loaderNeedsVersion(loader: string): boolean {
  return loader !== 'vanilla';
}

/** Returns the preferred loader build: the one flagged latest, else the first listed. */
export function defaultLoaderVersion(versions: LoaderVersion[]): string {
  return versions.find(version => version.latest)?.version ?? versions[0]?.version ?? '';
}

/** Why the wizard cannot continue with this selection, or undefined when it can. */
export function minecraftSelectionError(loader: string, minecraftVersion: string, loaderVersion: string): string | undefined {
  if (!minecraftLoaders.includes(loader as MinecraftLoader)) return 'Choose a server type.';
  if (!/^\d{1,2}\.\d{1,2}(\.\d{1,2})?$/.test(minecraftVersion)) return 'Choose a Minecraft version.';
  if (loaderNeedsVersion(loader) && !loaderVersion) return `Choose a ${loaderLabel(loader)} version.`;
  if (!loaderNeedsVersion(loader) && loaderVersion) return 'Vanilla has no loader version.';
  return undefined;
}

export type JavaInfo = { found: boolean; major: number; required_major: number };

export function javaAdvice(java?: JavaInfo): { tone: 'ok' | 'warning' | 'danger'; text: string } | undefined {
  if (!java) return undefined;
  if (!java.found) return { tone: 'danger', text: `Java was not found. This Minecraft version needs Java ${java.required_major} or newer on this host (JAVA_HOME or PATH).` };
  if (java.major > 0 && java.major < java.required_major) return { tone: 'warning', text: `This version needs Java ${java.required_major} or newer; this host has Java ${java.major}. Installation can succeed but the server will not start.` };
  return { tone: 'ok', text: java.major > 0 ? `Java ${java.major} detected (needs ${java.required_major}+).` : `Java detected (needs ${java.required_major}+).` };
}

export function formatModSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return 'Unknown';
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KiB`;
  return `${bytes} B`;
}

/** True when name has a safe base name and one of the accepted extensions (default: .jar). */
export function validModFileName(name: string, extensions: string[] = ['.jar']): boolean {
  const lower = name.toLowerCase();
  const extension = extensions.find(candidate => lower.endsWith(candidate));
  return !!extension && /^[A-Za-z0-9][A-Za-z0-9 ._+()[\]-]{0,150}$/.test(name.slice(0, name.length - extension.length));
}

export type ModsLayout = { game: string; directory: string; extensions: string[] };

export const defaultModsLayout: ModsLayout = { game: 'minecraft', directory: 'mods', extensions: ['.jar'] };

/** "a .jar file", "a .jar or .zip file". */
export function modsExtensionText(extensions: string[]): string {
  return extensions.length === 1 ? extensions[0] : extensions.slice(0, -1).join(', ') + ' or ' + extensions[extensions.length - 1];
}

/** Value for the file input's accept attribute. */
export function modsAcceptAttribute(extensions: string[]): string {
  return extensions.join(',');
}

/** A mod whose declared loaders exclude the server's loader will be ignored or fail at start. */
export function modCompatibility(serverLoader: string | undefined, mod: Pick<MinecraftMod, 'loaders'>): 'compatible' | 'mismatch' | 'unknown' {
  if (!serverLoader || !mod.loaders?.length) return 'unknown';
  return mod.loaders.includes(serverLoader) ? 'compatible' : 'mismatch';
}

export function filterMods(mods: MinecraftMod[], query: string): MinecraftMod[] {
  const term = query.trim().toLowerCase();
  if (!term) return mods;
  return mods.filter(mod => [mod.file_name, mod.name ?? '', mod.id ?? '', mod.version ?? ''].join(' ').toLowerCase().includes(term));
}

export function modDisplayName(mod: MinecraftMod): string {
  return mod.name || mod.id || mod.file_name.replace(/\.(jar|zip)(\.disabled)?$/i, '');
}

export const disabledSuffix = '.disabled';

export function modEnabled(mod: Pick<MinecraftMod, 'disabled'>): boolean {
  return !mod.disabled;
}

/** Counts shown above the list, e.g. "5 installed · 2 disabled". */
export function modCountLabel(mods: Pick<MinecraftMod, 'disabled'>[]): string {
  const disabled = mods.filter(mod => mod.disabled).length;
  return `${mods.length} installed${disabled ? ` · ${disabled} disabled` : ''}`;
}

/** Only enabled mods are loaded, so only they can conflict with the server loader. */
export function loadedModMismatches(serverLoader: string | undefined, mods: MinecraftMod[]): number {
  return mods.filter(mod => modEnabled(mod) && modCompatibility(serverLoader, mod) === 'mismatch').length;
}

export function modsUploadPath(serverID: string, overwrite: boolean): string {
  return `/servers/${serverID}/mods${overwrite ? '?overwrite=true' : ''}`;
}

export function modsStatePath(serverID: string): string {
  return `/servers/${serverID}/mods`;
}

export function modsDeletePath(serverID: string, fileName: string): string {
  return `/servers/${serverID}/mods?file=${encodeURIComponent(fileName)}`;
}

export function minecraftVersionsPath(loader: string, minecraftVersion?: string): string {
  const query = new URLSearchParams({ loader });
  if (minecraftVersion) query.set('minecraft_version', minecraftVersion);
  return `/minecraft/versions?${query.toString()}`;
}
