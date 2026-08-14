export type ServerLifecycleAction = 'start' | 'stop' | 'restart' | 'kill' | 'delete';

export function serverActionDisabled(action: ServerLifecycleAction, state: string): boolean {
  if (action === 'delete') return false;
  const active = state === 'running' || state === 'starting' || state === 'stopping';
  return action === 'start' ? active : state !== 'running';
}
