import type { RuntimeType } from './server-create-helpers';

export type ContainerImageAvailability = 'available' | 'missing' | 'engine_unavailable';
export type ContainerPullState = 'idle' | 'pulling' | 'failed';

export type ContainerDetailPort = {
  bind_address?: string;
  port: number;
  protocol: string;
  container_port?: number;
};

export function isContainerRuntime(runtimeType: RuntimeType | string | undefined): boolean {
  return runtimeType === 'container';
}

export function imageAvailabilityLabel(value: ContainerImageAvailability | string | undefined): string {
  if (value === 'available') return 'Available';
  if (value === 'missing') return 'Image not available locally';
  if (value === 'engine_unavailable') return 'Container engine unavailable';
  return 'Unavailable';
}

export function engineAvailabilityLabel(value: ContainerImageAvailability | string | undefined): string {
  if (value === 'available' || value === 'missing') return 'Reachable';
  if (value === 'engine_unavailable') return 'Unavailable';
  return 'Unavailable';
}

export function imageAvailabilityTone(value: ContainerImageAvailability | string | undefined): 'running' | 'degraded' | 'unknown' {
  if (value === 'available') return 'running';
  if (value === 'missing') return 'degraded';
  return 'unknown';
}

export function pullStateLabel(value: ContainerPullState | string | undefined): string {
  if (value === 'pulling') return 'Pulling image…';
  if (value === 'failed') return 'Image pull failed';
  return '';
}

export function pullStateTone(value: ContainerPullState | string | undefined): 'starting' | 'crashed' | 'running' | undefined {
  if (value === 'pulling') return 'starting';
  if (value === 'failed') return 'crashed';
  return value === 'idle' ? 'running' : undefined;
}

export function canPullContainer(runtimeType: RuntimeType | string | undefined, availability: ContainerImageAvailability | string | undefined, pullState: ContainerPullState | string | undefined, canEdit: boolean): boolean {
  return isContainerRuntime(runtimeType) && canEdit && (availability === 'available' || availability === 'missing') && pullState !== 'pulling';
}

export function formatCPULimit(millicores: number | undefined): string {
  return Number.isFinite(millicores) ? `${millicores} millicores` : 'Unavailable';
}

export function formatMemoryLimit(bytes: number | undefined): string {
  if (!Number.isFinite(bytes)) return 'Unavailable';
  const mebibytes = bytes! / (1024 * 1024);
  return Number.isInteger(mebibytes) ? `${mebibytes} MiB` : `${mebibytes.toFixed(1)} MiB`;
}

export function formatContainerPortMapping(port: ContainerDetailPort): string {
  const bind = port.bind_address || '0.0.0.0';
  const protocol = port.protocol.toUpperCase();
  const target = port.container_port || port.port;
  return `${bind}:${port.port}/${protocol} → ${target}/${protocol}`;
}

export function containerPullRequest(serverID: string, csrfToken: string): { path: string; init: RequestInit } {
  return { path: `/servers/${serverID}/container/pull`, init: { method: 'POST', headers: { 'X-CSRF-Token': csrfToken } } };
}
