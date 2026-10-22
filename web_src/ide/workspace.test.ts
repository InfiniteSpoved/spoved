import {expect, test} from 'vitest';
import {ExistsError, NotFoundError, toBase64, Workspace, type TreeEntry} from './workspace.ts';

const enc = (text: string) => new TextEncoder().encode(text);
const dec = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

const blobs: Record<string, string> = {a1: 'package main\n', b2: '# readme\n', c3: 'guide\n'};
const entries: TreeEntry[] = [
  {path: 'src', type: 'tree', mode: '040000', sha: 't1', size: 0},
  {path: 'src/main.go', type: 'blob', mode: '100644', sha: 'a1', size: 13},
  {path: 'README.md', type: 'blob', mode: '100644', sha: 'b2', size: 9},
  {path: 'docs', type: 'tree', mode: '040000', sha: 't2', size: 0},
  {path: 'docs/guide.md', type: 'blob', mode: '100644', sha: 'c3', size: 6},
];

function newWorkspace() {
  let reads = 0;
  const ws = new Workspace('abc', entries, async (entry) => {
    reads++;
    return enc(blobs[entry.sha]);
  });
  return {ws, reads: () => reads};
}

test('reads the committed tree once per blob', async () => {
  const {ws, reads} = newWorkspace();
  expect(ws.readDirectory('').sort()).toEqual([['README.md', 'file'], ['docs', 'directory'], ['src', 'directory']]);
  expect(dec(await ws.readFile('src/main.go'))).toBe('package main\n');
  await ws.readFile('src/main.go');
  expect(reads()).toBe(1);
  await expect(ws.readFile('missing')).rejects.toThrow(NotFoundError);
  expect(() => ws.readDirectory('missing')).toThrow(NotFoundError);
  expect(ws.changes()).toEqual([]);
});

test('edit, add and delete', async () => {
  const {ws} = newWorkspace();
  await ws.writeFile('src/main.go', enc('package main\n// x\n'));
  await ws.writeFile('src/new.go', enc('package main\n'));
  ws.delete('docs/guide.md');
  expect(ws.changes()).toEqual([
    {path: 'docs/guide.md', kind: 'deleted'},
    {path: 'src/main.go', kind: 'modified'},
    {path: 'src/new.go', kind: 'added'},
  ]);
  expect(ws.isDirectory('docs')).toBe(false);
  expect(ws.operations()).toEqual([
    {operation: 'delete', path: 'docs/guide.md', sha: 'c3'},
    {operation: 'update', path: 'src/main.go', sha: 'a1', content: toBase64(enc('package main\n// x\n'))},
    {operation: 'create', path: 'src/new.go', content: toBase64(enc('package main\n'))},
  ]);
});

test('writing the committed content back is no change', async () => {
  const {ws} = newWorkspace();
  await ws.writeFile('README.md', enc('changed'));
  await ws.writeFile('README.md', enc('# readme\n'));
  expect(ws.changes()).toEqual([]);
});

test('write refuses what the flags forbid', async () => {
  const {ws} = newWorkspace();
  await expect(ws.writeFile('nope', enc(''), {create: false})).rejects.toThrow(NotFoundError);
  await expect(ws.writeFile('README.md', enc(''), {overwrite: false})).rejects.toThrow(ExistsError);
  await expect(ws.writeFile('src', enc(''))).rejects.toThrow(ExistsError);
});

test('rename a file keeps its origin', async () => {
  const {ws} = newWorkspace();
  await ws.rename('README.md', 'README2.md');
  expect(ws.changes()).toEqual([{path: 'README2.md', kind: 'renamed', from: 'README.md'}]);
  expect(ws.operations()).toEqual([
    {operation: 'update', path: 'README2.md', from_path: 'README.md', sha: 'b2', content: toBase64(enc('# readme\n'))},
  ]);
  // renamed twice: still one rename from the committed path
  await ws.rename('README2.md', 'docs/README.md');
  expect(ws.changes()).toEqual([{path: 'docs/README.md', kind: 'renamed', from: 'README.md'}]);
  await expect(ws.rename('docs/README.md', 'src/main.go')).rejects.toThrow(ExistsError);
});

test('rename a directory moves its files', async () => {
  const {ws} = newWorkspace();
  await ws.rename('src', 'cmd');
  expect(ws.isDirectory('src')).toBe(false);
  expect(ws.changes()).toEqual([{path: 'cmd/main.go', kind: 'renamed', from: 'src/main.go'}]);
  await expect(ws.rename('cmd', 'cmd/inner')).rejects.toThrow(ExistsError);
});

test('empty directories exist until a file lands in them', async () => {
  const {ws} = newWorkspace();
  ws.createDirectory('lib/util');
  expect(ws.isDirectory('lib/util')).toBe(true);
  expect(ws.changes()).toEqual([]);
  await ws.writeFile('lib/util/a.go', enc('package util\n'));
  expect(ws.dirs.size).toBe(0);
  expect(ws.isDirectory('lib/util')).toBe(true);
  expect(() => ws.createDirectory('lib')).toThrow(ExistsError);
});

test('revert drops one change', async () => {
  const {ws} = newWorkspace();
  await ws.rename('README.md', 'README2.md');
  ws.delete('docs/guide.md');
  ws.revert('README2.md');
  ws.revert('docs/guide.md');
  expect(ws.changes()).toEqual([]);
  expect(ws.isFile('README.md')).toBe(true);
  expect(ws.isFile('README2.md')).toBe(false);
});

test('save and restore', async () => {
  const {ws} = newWorkspace();
  await ws.writeFile('src/main.go', enc('x'));
  await ws.rename('README.md', 'README2.md');
  ws.delete('docs/guide.md');
  const saved = ws.save();
  const {ws: other} = newWorkspace();
  other.restore(saved);
  expect(other.changes()).toEqual(ws.changes());
  other.clear();
  expect(other.changes()).toEqual([]);
});

test('toBase64 handles large and binary content', () => {
  const bytes = new Uint8Array(100000).map((_, i) => i % 256);
  expect(atob(toBase64(bytes)).length).toBe(bytes.length);
  expect(toBase64(enc('hi'))).toBe('aGk=');
});
