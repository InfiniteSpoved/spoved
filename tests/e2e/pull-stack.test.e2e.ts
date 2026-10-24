// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// @watch start
// templates/repo/issue/view_content.tmpl
// templates/repo/issue/view_content/stack.tmpl
// templates/repo/issue/view_content/stack_merge.tmpl
// templates/repo/issue/view_content/pull.tmpl
// web_src/js/features/repo-issue-pr-status.js
// templates/shared/issuelist.tmpl
// web_src/css/repo/pull-stack.css
// web_src/css/modules/dialog.css
// @watch end

import {expect, type APIRequestContext} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {adminAuth} from './shared/fake-data.ts';

test.use({user: 'user2'});

test('Stacked pull requests: panel, merge dialog and list marks', async ({page, request}) => {
  const api = '/api/v1/repos/user2/repo1';
  const id = Date.now();
  const lower = `stack-lower-${id}`;
  const upper = `stack-upper-${id}`;
  const side = `stack-side-${id}`;
  const create = async (branch: string, from: string | null) => {
    const data: Record<string, string> = {content: btoa(branch), new_branch: branch};
    if (from) data.branch = from;
    expect((await request.post(`${api}/contents/${branch}.txt`, {headers: adminAuth(), data})).ok()).toBeTruthy();
  };
  const open = async (head: string, base: string, title: string) => {
    const res = await request.post(`${api}/pulls`, {headers: adminAuth(), data: {head, base, title}});
    expect(res.ok()).toBeTruthy();
    return (await res.json()).number as number;
  };

  await create(lower, null);
  await create(upper, lower);
  await create(side, lower);
  const lowerNumber = await open(lower, 'master', `Lower ${id}`);
  const upperNumber = await open(upper, lower, `Upper ${id}`);
  const sideNumber = await open(side, lower, `Side ${id}`);

  try {
    await page.goto(`/user2/repo1/pulls/${upperNumber}`);
    const panel = page.locator('details.pull-stack');
    await expect(panel).toBeVisible();
    await expect(panel.locator('summary')).toContainText('2 pull requests into master');
    const rows = panel.locator('li.pull-stack-row');
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText(`#${lowerNumber} Lower ${id}`);
    await expect(rows.nth(0)).not.toContainText(/^\s*1\s/); // the list announces its numbers
    await expect(rows.nth(1)).toHaveAttribute('aria-current', 'true');
    await expect(rows.nth(1)).toContainText('(this pull request)');
    await expect(rows.nth(1)).toContainText('Review pending');

    // the header toggles the list, which is how it shrinks on a phone
    await panel.locator('summary').click();
    await expect(rows.first()).toBeHidden();
    await panel.locator('summary').click();
    await expect(rows.first()).toBeVisible();

    const notice = page.locator('#pull-stack-merge');
    await expect(notice).toContainText(`This pull request is stacked on #${lowerNumber}`);
    await expect(page.getByRole('button', {name: 'Merge this pull request only'})).toBeVisible();
    const button = notice.getByRole('button', {name: 'Merge stack (2 pull requests) into master'});
    await button.scrollIntoViewIfNeeded();
    await expect(button).toBeInViewport();
    await button.click();

    const dialog = page.getByRole('dialog', {name: 'Merge 2 pull requests into master?'});
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('li')).toHaveCount(2);
    // the pull request on the same branch moves with them
    await expect(dialog).toContainText(`#${sideNumber} stays open and moves onto master.`);
    await expect(dialog.getByLabel('Merge style')).toBeVisible();
    await expect(dialog.getByLabel('Delete the 2 merged branches')).toBeVisible();
    await expect(dialog.getByRole('button', {name: 'Merge 2 pull requests'})).toBeVisible();
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toBeHidden();

    // the bottom of the stack has the panel but nothing to merge under it; it branches, so its children are indented
    await page.goto(`/user2/repo1/pulls/${lowerNumber}`);
    await expect(page.locator('details.pull-stack')).toBeVisible();
    await expect(page.locator('#pull-stack-merge')).toHaveCount(0);
    const branchRows = page.locator('details.pull-stack li.pull-stack-row');
    await expect(branchRows).toHaveCount(3);
    await expect(branchRows.nth(0)).not.toHaveClass(/is-branch/);
    await expect(branchRows.nth(1)).toHaveClass(/is-branch/);
    await expect(branchRows.nth(2)).toHaveClass(/is-branch/);
    await expect(branchRows.nth(2)).toContainText(`also on #${lowerNumber}`);

    await page.goto('/user2/repo1/pulls');
    const marks = (number: number) => page.locator('#issue-list .flex-item').filter({has: page.getByRole('link', {name: `#${number}`, exact: true})}).locator('.pull-stack-mark');
    await expect(marks(lowerNumber)).toHaveText('Bottom of a stack');
    await expect(marks(upperNumber)).toHaveText(`Stacked on #${lowerNumber}`);
  } finally {
    for (const number of [sideNumber, upperNumber, lowerNumber]) {
      await request.patch(`${api}/pulls/${number}`, {headers: adminAuth(), data: {state: 'closed'}});
    }
    for (const branch of [side, upper, lower]) {
      await request.delete(`${api}/branches/${branch}`, {headers: adminAuth()});
    }
  }
});

// Helpers for the tests that really merge a stack. Every test makes its own files, branches and pull requests.
const stackApi = '/api/v1/repos/user2/repo1';

const stackHelpers = (request: APIRequestContext) => {
  const branches: string[] = [];
  const pulls: number[] = [];
  const write = async (branch: string, from: string | null, path: string, content: string, sha?: string) => {
    // from: the branch to start the new branch `branch` from; null writes to `branch` itself
    const data: Record<string, string> = {content: btoa(content), message: `${branch} ${path}`, branch: from ?? branch};
    if (from) {
      data.new_branch = branch;
      branches.push(branch);
    }
    if (sha) data.sha = sha;
    const res = await (sha ? request.put : request.post).call(request, `${stackApi}/contents/${path}`, {headers: adminAuth(), data});
    expect(res.ok()).toBeTruthy();
    return (await res.json()).content.sha as string;
  };
  const open = async (head: string, base: string, title: string) => {
    const res = await request.post(`${stackApi}/pulls`, {headers: adminAuth(), data: {head, base, title}});
    expect(res.ok()).toBeTruthy();
    const number = (await res.json()).number as number;
    pulls.push(number);
    return number;
  };
  const pull = async (number: number) => {
    const res = await request.get(`${stackApi}/pulls/${number}`, {headers: adminAuth()});
    expect(res.ok()).toBeTruthy();
    return await res.json();
  };
  const cleanup = async () => {
    for (const number of pulls) {
      await request.patch(`${stackApi}/pulls/${number}`, {headers: adminAuth(), data: {state: 'closed'}});
    }
    for (const branch of branches) {
      await request.delete(`${stackApi}/branches/${branch}`, {headers: adminAuth()});
    }
  };
  return {write, open, pull, cleanup};
};

test('Stacked pull requests: merge a stack', async ({page, request}) => {
  const id = Date.now();
  const h = stackHelpers(request);
  const [a, b, c, d] = ['a', 'b', 'c', 'd'].map((n) => `stack-merge-${n}-${id}`);
  const file = (n: string) => `stack-merge-${n}-${id}.txt`;

  try {
    await h.write(a, 'master', file('a'), 'a');
    await h.write(b, a, file('b'), 'b');
    await h.write(c, b, file('c'), 'c');
    await h.write(d, b, file('d'), 'd');
    const prA = await h.open(a, 'master', `Merge A ${id}`);
    const prB = await h.open(b, a, `Merge B ${id}`);
    const prC = await h.open(c, b, `Merge C ${id}`);
    const prD = await h.open(d, b, `Merge D ${id}`);

    await page.goto(`/user2/repo1/pulls/${prC}`);
    const notice = page.locator('#pull-stack-merge');
    await notice.getByRole('button', {name: 'Merge stack (3 pull requests) into master'}).click();
    const dialog = page.getByRole('dialog', {name: 'Merge 3 pull requests into master?'});
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('li')).toHaveCount(3);
    await expect(dialog).toContainText(`#${prD} stays open and moves onto master.`);
    const remove = dialog.getByLabel('Delete the 3 merged branches');
    await remove.check();
    await dialog.getByRole('button', {name: 'Merge 3 pull requests', exact: true}).click();

    await expect(page.locator('#flash-message.flash-message')).toContainText('Merged 3 pull requests into master.');

    for (const number of [prA, prB, prC]) {
      expect((await h.pull(number)).merged).toBe(true);
    }
    for (const n of ['a', 'b', 'c']) {
      expect((await request.get(`${stackApi}/contents/${file(n)}?ref=master`, {headers: adminAuth()})).ok()).toBeTruthy();
    }
    const side = await h.pull(prD);
    expect(side.state).toBe('open');
    expect(side.merged).toBe(false);
    expect(side.base.ref).toBe('master');
    for (const branch of [a, b, c]) {
      expect((await request.get(`${stackApi}/branches/${branch}`, {headers: adminAuth()})).status()).toBe(404);
    }
    expect((await request.get(`${stackApi}/branches/${d}`, {headers: adminAuth()})).ok()).toBeTruthy();
  } finally {
    await h.cleanup();
  }
});

test('Stacked pull requests: merge stops at a pull request that conflicts', async ({page, request}) => {
  const id = Date.now();
  const h = stackHelpers(request);
  const [a, b, c] = ['a', 'b', 'c'].map((n) => `stack-stop-${n}-${id}`);
  const path = `stack-stop-${id}.txt`;

  try {
    // squashed onto master, a is no ancestor of b: b conflicts with master after the retarget
    const sha = await h.write('master', null, path, 'one\n');
    const shaA1 = await h.write(a, 'master', path, 'one\ntwo\n', sha);
    const shaA2 = await h.write(a, null, path, 'one\ntwo\nthree\n', shaA1);
    await h.write(b, a, path, 'one\nTWO\nthree\n', shaA2);
    await h.write(c, b, `stack-stop-c-${id}.txt`, 'c');
    const prA = await h.open(a, 'master', `Stop A ${id}`);
    const prB = await h.open(b, a, `Stop B ${id}`);
    const prC = await h.open(c, b, `Stop C ${id}`);

    await page.goto(`/user2/repo1/pulls/${prC}`);
    await page.locator('#pull-stack-merge').getByRole('button', {name: 'Merge stack (3 pull requests) into master'}).click();
    const dialog = page.getByRole('dialog', {name: 'Merge 3 pull requests into master?'});
    await dialog.getByLabel('Merge style').selectOption('squash');
    await dialog.getByRole('button', {name: 'Merge 3 pull requests', exact: true}).click();

    const flash = page.locator('#flash-message.flash-message');
    await expect(flash).toContainText('Merged 1 of 3 pull requests, then stopped at');
    await expect(flash.getByRole('link', {name: `#${prB}`, exact: true}).first()).toBeVisible();
    await expect(flash).toContainText('it is not in a mergeable state, for example because of merge conflicts.');
    await expect(flash).toContainText(`#${prA} is merged into master.`);

    expect((await h.pull(prA)).merged).toBe(true);
    const middle = await h.pull(prB);
    expect(middle.merged).toBe(false);
    expect(middle.state).toBe('open');
    expect(middle.base.ref).toBe('master');
    const top = await h.pull(prC);
    expect(top.merged).toBe(false);
    expect(top.state).toBe('open');
  } finally {
    await h.cleanup();
  }
});
