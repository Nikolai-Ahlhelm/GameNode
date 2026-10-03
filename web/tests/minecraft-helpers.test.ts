import assert from 'node:assert/strict';
import test from 'node:test';
import { defaultLoaderVersion, filterMods, formatModSize, javaAdvice, loaderLabel, minecraftSelectionError, minecraftVersionsPath, modCompatibility, modDisplayName, modsDeletePath, modsUploadPath, validModFileName } from '../src/minecraft-helpers.ts';

test('selection requires a loader version for modded loaders only', () => {
  assert.equal(minecraftSelectionError('vanilla', '1.21.1', ''), undefined);
  assert.equal(minecraftSelectionError('vanilla', '1.21.1', '1.0'), 'Vanilla has no loader version.');
  assert.equal(minecraftSelectionError('fabric', '1.21.1', ''), 'Choose a Fabric version.');
  assert.equal(minecraftSelectionError('neoforge', '1.21.1', '21.1.77'), undefined);
  assert.equal(minecraftSelectionError('quilt', '1.21.1', '1'), 'Choose a server type.');
  assert.equal(minecraftSelectionError('forge', '', '47.4.0'), 'Choose a Minecraft version.');
  assert.equal(minecraftSelectionError('forge', '1.20.1; rm', '47.4.0'), 'Choose a Minecraft version.');
  assert.equal(minecraftSelectionError('forge', '26.3', '66.0.9'), undefined);
});

test('default loader version prefers the latest stable build', () => {
  assert.equal(defaultLoaderVersion([{ version: '2-beta', stable: false }, { version: '1', stable: true, latest: true }]), '1');
  assert.equal(defaultLoaderVersion([{ version: '9', stable: false }]), '9');
  assert.equal(defaultLoaderVersion([]), '');
});

test('java advice distinguishes missing, too old and fine', () => {
  assert.equal(javaAdvice(undefined), undefined);
  assert.equal(javaAdvice({ found: false, major: 0, required_major: 21 })?.tone, 'danger');
  assert.equal(javaAdvice({ found: true, major: 17, required_major: 21 })?.tone, 'warning');
  assert.equal(javaAdvice({ found: true, major: 25, required_major: 21 })?.tone, 'ok');
  assert.equal(javaAdvice({ found: true, major: 0, required_major: 21 })?.tone, 'ok');
});

test('mod file names are restricted to safe jar names', () => {
  for (const good of ['sodium.jar', 'Create-1.21.1_v6.0.JAR', 'a b (1).jar']) assert.equal(validModFileName(good), true, good);
  for (const bad of ['../x.jar', 'a/b.jar', 'x.exe', '.hidden.jar', 'x.jar.exe', 'a\\b.jar', '']) assert.equal(validModFileName(bad), false, bad);
});

test('mod compatibility compares declared loaders with the server loader', () => {
  assert.equal(modCompatibility('fabric', { loaders: ['fabric'] }), 'compatible');
  assert.equal(modCompatibility('fabric', { loaders: ['forge', 'neoforge'] }), 'mismatch');
  assert.equal(modCompatibility(undefined, { loaders: ['fabric'] }), 'unknown');
  assert.equal(modCompatibility('forge', {}), 'unknown');
});

test('mod filtering and display names', () => {
  const mods = [
    { file_name: 'sodium-0.6.jar', size: 1, modified_at: '', id: 'sodium', name: 'Sodium', version: '0.6' },
    { file_name: 'jei.jar', size: 1, modified_at: '' },
  ];
  assert.equal(filterMods(mods, '').length, 2);
  assert.deepEqual(filterMods(mods, ' SOD ').map(mod => mod.file_name), ['sodium-0.6.jar']);
  assert.equal(modDisplayName(mods[0]), 'Sodium');
  assert.equal(modDisplayName(mods[1]), 'jei');
});

test('paths are encoded and sizes are readable', () => {
  assert.equal(modsUploadPath('s1', false), '/servers/s1/mods');
  assert.equal(modsUploadPath('s1', true), '/servers/s1/mods?overwrite=true');
  assert.equal(modsDeletePath('s1', 'a b&c.jar'), '/servers/s1/mods?file=a%20b%26c.jar');
  assert.equal(minecraftVersionsPath('fabric'), '/minecraft/versions?loader=fabric');
  assert.equal(minecraftVersionsPath('forge', '1.20.1'), '/minecraft/versions?loader=forge&minecraft_version=1.20.1');
  assert.equal(formatModSize(512), '512 B');
  assert.equal(formatModSize(2048), '2 KiB');
  assert.equal(formatModSize(3 * 1024 * 1024), '3.0 MiB');
  assert.equal(formatModSize(-1), 'Unknown');
  assert.equal(loaderLabel('neoforge'), 'NeoForge');
});

test('disabled mods are counted, named and excluded from loader mismatches', async () => {
  const { loadedModMismatches, modCountLabel, modDisplayName, modEnabled, modsStatePath } = await import('../src/minecraft-helpers.ts');
  const mods = [
    { file_name: 'a.jar', size: 1, modified_at: '', loaders: ['forge'] },
    { file_name: 'b.jar.disabled', size: 1, modified_at: '', loaders: ['forge'], disabled: true },
    { file_name: 'c.jar', size: 1, modified_at: '', loaders: ['fabric'] },
  ];
  assert.equal(modCountLabel(mods), '3 installed · 1 disabled');
  assert.equal(modCountLabel(mods.slice(0, 1)), '1 installed');
  assert.equal(loadedModMismatches('fabric', mods), 1);
  assert.equal(loadedModMismatches(undefined, mods), 0);
  assert.equal(modEnabled(mods[1]), false);
  assert.equal(modDisplayName(mods[1]), 'b');
  assert.equal(modsStatePath('s1'), '/servers/s1/mods');
});
