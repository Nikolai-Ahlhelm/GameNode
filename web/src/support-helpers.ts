export const supportFilename = (header: string | null): string => {
  const match = header?.match(/filename="?([^";]+)"?/i);
  const candidate = match?.[1] ?? '';
  return /^gamenode-support-[A-Za-z0-9._-]+\.zip$/.test(candidate) ? candidate : 'gamenode-support.zip';
};
