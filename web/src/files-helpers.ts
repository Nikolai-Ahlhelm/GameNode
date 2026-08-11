const textExtensions = new Set(['txt', 'json', 'yaml', 'yml', 'xml', 'ini', 'cfg', 'properties', 'conf', 'md']);
const readOnlyExtensions = new Set(['log']);

export function joinRelativePath(parent: string, child: string): string {
  return [parent, child].filter(Boolean).join('/');
}

export function parentRelativePath(value: string): string {
  const parts = value.split('/').filter(Boolean);
  parts.pop();
  return parts.join('/');
}

export function breadcrumbs(value: string): Array<{ label: string; path: string }> {
  const parts = value.split('/').filter(Boolean);
  return [{ label: 'Root', path: '' }, ...parts.map((label, index) => ({ label, path: parts.slice(0, index + 1).join('/') }))];
}

export function classifyFile(name: string): { text: boolean; readOnly: boolean; language: string } {
  const extension = name.includes('.') ? name.split('.').pop()!.toLowerCase() : '';
  const readOnly = readOnlyExtensions.has(extension);
  if (readOnly) return { text: true, readOnly: true, language: 'plaintext' };
  if (!textExtensions.has(extension)) return { text: false, readOnly: false, language: 'plaintext' };
  const languages: Record<string, string> = { yml: 'yaml', yaml: 'yaml', properties: 'properties', cfg: 'ini', conf: 'ini', txt: 'plaintext' };
  return { text: true, readOnly: false, language: languages[extension] ?? extension };
}

export function isSafeRelativePath(value: string): boolean {
  const normalized = value.replaceAll('\\', '/');
  return Boolean(normalized) && !normalized.startsWith('/') && !/^[A-Za-z]:/.test(normalized) && !normalized.split('/').some(part => part === '..');
}

export function formatFileSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}
