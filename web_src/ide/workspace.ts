// The Web IDE's working tree: the committed tree of one commit plus the changes made in the browser.
// It knows nothing about VS Code, so it is tested on its own.

export type TreeEntry = {
  path: string;
  type: 'blob' | 'tree' | 'commit';
  mode: string;
  sha: string;
  size: number;
};

export type ChangeKind = 'added' | 'modified' | 'deleted' | 'renamed';
export type Change = {path: string; kind: ChangeKind; from?: string};

export type FileOperation = {
  operation: 'create' | 'update' | 'delete';
  path: string;
  content?: string;
  sha?: string;
  from_path?: string;
};

// what a session keeps in IndexedDB, so a reload doesn't lose work
export type SavedChanges = {
  commitID: string;
  modified: [string, Uint8Array][];
  deleted: string[];
  renamedFrom: [string, string][];
};

export class NotFoundError extends Error {}
export class ExistsError extends Error {}

export type BlobReader = (entry: TreeEntry) => Promise<Uint8Array>;

const parentOf = (path: string) => path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
const isUnder = (path: string, dir: string) => dir === '' || path === dir || path.startsWith(`${dir}/`);

export function sameBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

export function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}

export class Workspace {
  commitID: string;
  readonly base = new Map<string, TreeEntry>();
  // working-tree content that differs from the base, by path
  readonly modified = new Map<string, Uint8Array>();
  // base files that are gone from the working tree
  readonly deleted = new Set<string>();
  // a renamed file's new path → its base path
  readonly renamedFrom = new Map<string, string>();
  // empty directories made in the IDE (git doesn't keep them)
  readonly dirs = new Set<string>();
  private readonly contents = new Map<string, Uint8Array>(); // base content by blob sha
  private readonly readBlob: BlobReader;

  constructor(commitID: string, entries: TreeEntry[], readBlob: BlobReader) {
    this.commitID = commitID;
    this.readBlob = readBlob;
    for (const entry of entries) this.base.set(entry.path, entry);
  }

  private baseFile(path: string): TreeEntry | undefined {
    const entry = this.base.get(path);
    return entry && entry.type !== 'tree' && !this.deleted.has(path) ? entry : undefined;
  }

  isFile(path: string): boolean {
    return this.modified.has(path) || this.baseFile(path) !== undefined;
  }

  isDirectory(path: string): boolean {
    if (path === '' || this.dirs.has(path)) return true;
    if (this.base.get(path)?.type === 'tree') {
      if (![...this.deleted].some((p) => isUnder(p, path)) || this.files().some((p) => isUnder(p, path))) return true;
    }
    return [...this.modified.keys()].some((p) => p.startsWith(`${path}/`));
  }

  // every file of the working tree
  files(): string[] {
    const files = [...this.modified.keys()];
    for (const [path, entry] of this.base) {
      if (entry.type !== 'tree' && !this.deleted.has(path) && !this.modified.has(path)) files.push(path);
    }
    return files;
  }

  entry(path: string): TreeEntry | undefined {
    return this.modified.has(path) ? undefined : this.baseFile(path);
  }

  size(path: string): number {
    return this.modified.get(path)?.length ?? this.baseFile(path)?.size ?? 0;
  }

  readDirectory(dir: string): [string, 'file' | 'directory'][] {
    if (!this.isDirectory(dir)) throw new NotFoundError(dir);
    const children = new Map<string, 'file' | 'directory'>();
    const prefix = dir === '' ? '' : `${dir}/`;
    const add = (path: string, isFile: boolean) => {
      if (!path.startsWith(prefix) || path === dir) return;
      const rest = path.slice(prefix.length);
      const slash = rest.indexOf('/');
      if (slash === -1) {
        children.set(rest, isFile ? 'file' : 'directory');
      } else {
        children.set(rest.slice(0, slash), 'directory');
      }
    };
    for (const path of this.files()) add(path, true);
    for (const path of this.dirs) add(path, false);
    return [...children];
  }

  async readBase(entry: TreeEntry): Promise<Uint8Array> {
    let content = this.contents.get(entry.sha);
    if (!content) {
      content = await this.readBlob(entry);
      this.contents.set(entry.sha, content);
    }
    return content;
  }

  async readFile(path: string): Promise<Uint8Array> {
    const modified = this.modified.get(path);
    if (modified) return modified;
    const entry = this.baseFile(path);
    if (!entry) throw new NotFoundError(path);
    return this.readBase(entry);
  }

  async writeFile(path: string, content: Uint8Array, {create = true, overwrite = true} = {}) {
    const exists = this.isFile(path);
    if (!exists && !create) throw new NotFoundError(path);
    if (exists && !overwrite) throw new ExistsError(path);
    if (this.isDirectory(path)) throw new ExistsError(path);
    const base = this.base.get(path);
    if (base && base.type !== 'tree' && !this.renamedFrom.has(path) && sameBytes(content, await this.readBase(base))) {
      // back to the committed content
      this.modified.delete(path);
      this.deleted.delete(path);
    } else {
      this.modified.set(path, content);
    }
    for (let dir = parentOf(path); dir !== ''; dir = parentOf(dir)) this.dirs.delete(dir);
  }

  createDirectory(path: string) {
    if (this.isFile(path) || this.isDirectory(path)) throw new ExistsError(path);
    this.dirs.add(path);
  }

  delete(path: string) {
    if (!this.isFile(path) && !this.isDirectory(path)) throw new NotFoundError(path);
    for (const file of this.files()) {
      if (!isUnder(file, path)) continue;
      this.modified.delete(file);
      this.renamedFrom.delete(file);
      if (this.base.get(file)?.type !== 'tree' && this.base.has(file)) this.deleted.add(file);
    }
    for (const dir of [...this.dirs]) {
      if (isUnder(dir, path)) this.dirs.delete(dir);
    }
  }

  async rename(from: string, to: string, overwrite = false) {
    if (!this.isFile(from) && !this.isDirectory(from)) throw new NotFoundError(from);
    if (isUnder(to, from)) throw new ExistsError(to);
    if (!overwrite && (this.isFile(to) || this.isDirectory(to))) throw new ExistsError(to);
    const moves = this.files().filter((file) => isUnder(file, from)).map((file) => [file, to + file.slice(from.length)]);
    const contents = await Promise.all(moves.map(([file]) => this.readFile(file)));
    const origins = moves.map(([file]) => this.renamedFrom.get(file) ?? (this.baseFile(file) ? file : undefined));
    const dirs = [...this.dirs].filter((dir) => isUnder(dir, from));
    if (overwrite) this.deleteIfExists(to);
    this.delete(from);
    moves.forEach(([, target], i) => {
      this.modified.set(target, contents[i]);
      this.deleted.delete(target);
      if (origins[i] && origins[i] !== target && !this.baseFile(target)) this.renamedFrom.set(target, origins[i]);
    });
    for (const dir of dirs) this.dirs.add(to + dir.slice(from.length));
    if (moves.length === 0 && dirs.length === 0) this.dirs.add(to);
  }

  private deleteIfExists(path: string) {
    if (this.isFile(path) || this.isDirectory(path)) this.delete(path);
  }

  changes(): Change[] {
    const changes: Change[] = [];
    const renameSources = new Set(this.renamedFrom.values());
    for (const path of this.modified.keys()) {
      const from = this.renamedFrom.get(path);
      if (from && !this.baseFile(path)) {
        changes.push({path, kind: 'renamed', from});
      } else {
        changes.push({path, kind: this.baseFile(path) || this.deleted.has(path) ? 'modified' : 'added'});
      }
    }
    for (const path of this.deleted) {
      if (!renameSources.has(path) && !this.modified.has(path)) changes.push({path, kind: 'deleted'});
    }
    return changes.sort((a, b) => a.path.localeCompare(b.path));
  }

  // the operations of one commit through the web route, which applies them all or none
  operations(): FileOperation[] {
    return this.changes().map(({path, kind, from}): FileOperation => {
      switch (kind) {
        case 'added':
          return {operation: 'create', path, content: toBase64(this.modified.get(path))};
        case 'renamed':
          return {operation: 'update', path, from_path: from, sha: this.base.get(from).sha, content: toBase64(this.modified.get(path))};
        case 'deleted':
          return {operation: 'delete', path, sha: this.base.get(path).sha};
        default:
          return {operation: 'update', path, sha: this.base.get(path).sha, content: toBase64(this.modified.get(path))};
      }
    });
  }

  // drops the change of one path: the file is back as committed
  revert(path: string) {
    const from = this.renamedFrom.get(path);
    this.modified.delete(path);
    this.renamedFrom.delete(path);
    this.deleted.delete(path);
    if (from) this.deleted.delete(from);
  }

  save(): SavedChanges {
    return {commitID: this.commitID, modified: [...this.modified], deleted: [...this.deleted], renamedFrom: [...this.renamedFrom]};
  }

  restore(saved: SavedChanges) {
    for (const [path, content] of saved.modified) this.modified.set(path, content);
    for (const path of saved.deleted) this.deleted.add(path);
    for (const [path, from] of saved.renamedFrom) this.renamedFrom.set(path, from);
  }

  clear() {
    this.modified.clear();
    this.deleted.clear();
    this.renamedFrom.clear();
    this.dirs.clear();
  }
}
