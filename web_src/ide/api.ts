// The web routes the Web IDE talks to; they run with the signed-in session, no token is involved.
import {request as fetchRequest} from '../js/modules/fetch.js';
import type {FileOperation, TreeEntry} from './workspace.ts';

export type IDEConfig = {
  repoLink: string;
  repoName: string;
  refType: 'branch' | 'tag' | 'commit';
  refName: string;
  commitID: string;
  treePath: string;
  defaultBranch: string;
  canCommit: boolean;
  canCreateBranch: boolean;
  canCreatePull: boolean;
  maxFileSize: number;
  theme: 'light' | 'dark' | 'auto' | 'high-contrast';
  themeName: string;
  assetUrlPrefix: string;
  strings: Record<string, string>;
};

export type SearchMatch = {path: string; lines: number[]; codes: string[]};

export class RequestError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export const escapePath = (path: string) => path.split('/').map(encodeURIComponent).join('/');

async function request(url: string, opts: {method?: string; data?: unknown} = {}): Promise<Response> {
  const resp = await fetchRequest(url, {...opts, headers: {Accept: 'application/json'}});
  if (resp.ok) return resp;
  let body: {error?: string; errorMessage?: string} = {};
  try {
    body = await resp.json();
  } catch {
    // not JSON, the status tells enough
  }
  throw new RequestError(resp.status, body.error ?? '', body.errorMessage || resp.statusText);
}

// the commit's whole tree, one page after the other
export async function fetchTree(repoLink: string, commitID: string): Promise<TreeEntry[]> {
  const entries: TreeEntry[] = [];
  for (let page = 1; ; page++) {
    const tree = await (await request(`${repoLink}/ide/-/tree/${commitID}?page=${page}`)).json();
    entries.push(...(tree.tree ?? []));
    if (!tree.truncated || entries.length >= tree.total_count || !tree.tree?.length) return entries;
  }
}

export async function fetchBlob(repoLink: string, sha: string): Promise<Uint8Array> {
  return new Uint8Array(await (await request(`${repoLink}/raw/blob/${sha}`)).arrayBuffer());
}

export async function search(repoLink: string, commitID: string, query: string, regexp: boolean): Promise<SearchMatch[]> {
  const params = new URLSearchParams({q: query});
  if (regexp) params.set('regexp', 'true');
  return (await request(`${repoLink}/ide/-/search/${commitID}?${params}`)).json();
}

export type CommitRequest = {
  last_commit: string;
  new_branch?: string;
  message: string;
  signoff: boolean;
  files: FileOperation[];
};

export async function commit(repoLink: string, branch: string, req: CommitRequest): Promise<{branch: string; commit: string}> {
  return (await request(`${repoLink}/ide/branch/${escapePath(branch)}`, {method: 'POST', data: req})).json();
}

// opens a pull request through the compare page's form, so every check of that page applies; returns its link
export async function createPull(repoLink: string, base: string, head: string, title: string, content: string): Promise<string> {
  const form = new FormData();
  form.set('title', title);
  form.set('content', content);
  const resp = await request(`${repoLink}/compare/${escapePath(base)}...${escapePath(head)}`, {method: 'POST', data: form});
  return (await resp.json()).redirect;
}
