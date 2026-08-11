export function hasCapability(capabilities: readonly string[] | undefined, permission: string): boolean {
  return capabilities?.includes(permission) ?? false;
}

export function canSendConsole(capabilities: readonly string[] | undefined): boolean {
  return hasCapability(capabilities, 'Console.Send');
}
