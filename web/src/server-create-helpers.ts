export type RuntimeType = 'native' | 'container';

export type ContainerCreateDraft = {
  image: string;
  command: string;
  cpuLimitMillis: string;
  memoryLimitMiB: string;
};

export type ServerCreateValues = {
  runtime_type: RuntimeType;
  container?: unknown;
  executable: string;
  arguments: string[];
  environment_variables: Record<string, string>;
  [key: string]: unknown;
};

const bytesPerMiB = 1024 * 1024;

export function defaultContainerCreateDraft(): ContainerCreateDraft {
  return { image: '', command: '', cpuLimitMillis: '1000', memoryLimitMiB: '1024' };
}

export function containerDraftFromValues(values?: { container?: { image: string; command: string[]; cpu_limit_millis: number; memory_limit_bytes: number } }): ContainerCreateDraft {
  if (!values?.container) return defaultContainerCreateDraft();
  return {
    image: values.container.image,
    command: values.container.command.join('\n'),
    cpuLimitMillis: String(values.container.cpu_limit_millis),
    memoryLimitMiB: String(values.container.memory_limit_bytes / bytesPerMiB),
  };
}

export function buildServerCreatePayload(values: ServerCreateValues, argumentsList: string[], environmentVariables: Record<string, string>, containerDraft: ContainerCreateDraft): Record<string, unknown> {
  if (values.runtime_type === 'native') {
    const { container: _container, ...native } = values;
    return { ...native, arguments: argumentsList, environment_variables: environmentVariables };
  }

  const image = containerDraft.image.trim();
  const cpuLimitMillis = Number(containerDraft.cpuLimitMillis);
  const memoryLimitMiB = Number(containerDraft.memoryLimitMiB);
  if (!image) throw new Error('Container image is required');
  if (!Number.isInteger(cpuLimitMillis) || cpuLimitMillis < 10 || cpuLimitMillis > 1_000_000) throw new Error('CPU limit must be a whole number between 10 and 1000000 millicores');
  if (!Number.isInteger(memoryLimitMiB) || memoryLimitMiB < 16 || memoryLimitMiB > 1024 * 1024 * 1024) throw new Error('Memory limit must be a whole number between 16 MiB and 1073741824 MiB');

  const { container: _container, executable: _executable, arguments: _arguments, environment_variables: _environment, ...shared } = values;
  return {
    ...shared,
    container: {
      image,
      command: containerDraft.command.split('\n').filter(Boolean),
      cpu_limit_millis: cpuLimitMillis,
      memory_limit_bytes: memoryLimitMiB * bytesPerMiB,
    },
  };
}
