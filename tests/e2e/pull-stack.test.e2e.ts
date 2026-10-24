// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// @watch start
// templates/repo/issue/view_content.tmpl
// templates/repo/issue/view_content/stack.tmpl
// templates/repo/issue/view_content/stack_merge.tmpl
// templates/shared/issuelist.tmpl
// web_src/css/repo/pull-stack.css
// web_src/css/modules/dialog.css
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {adminAuth} from './shared/fake-data.ts';

test.use({user: 'user2'});

test('Stacked pull requests: panel, merge dialog and list marks', async ({page, request}) => {
  const api = '/api/v1/repos/user2/repo1';
  const id = Date.now();
  const lower = `stack-lower-${id}`;
  const upper = `stack-upper-${id}`;
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
  const lowerNumber = await open(lower, 'master', `Lower ${id}`);
  const upperNumber = await open(upper, lower, `Upper ${id}`);

  try {
    await page.goto(`/user2/repo1/pulls/${upperNumber}`);
    const panel = page.locator('details.pull-stack');
    await expect(panel).toBeVisible();
    await expect(panel.locator('summary')).toContainText('2 pull requests into master');
    const rows = panel.locator('li.pull-stack-row');
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText(`#${lowerNumber} Lower ${id}`);
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
    const button = notice.getByRole('button', {name: 'Merge stack (2 pull requests) into master'});
    await button.scrollIntoViewIfNeeded();
    await expect(button).toBeInViewport();
    await button.click();

    const dialog = page.locator('dialog#merge-stack');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('header')).toHaveText('Merge 2 pull requests into master?');
    await expect(dialog.locator('li')).toHaveCount(2);
    await expect(dialog.getByLabel('Merge style')).toBeVisible();
    await expect(dialog.getByLabel('Delete the 2 merged branches')).toBeVisible();
    await expect(dialog.getByRole('button', {name: 'Merge 2 pull requests'})).toBeVisible();
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toBeHidden();

    // the bottom of the stack has the panel but nothing to merge under it
    await page.goto(`/user2/repo1/pulls/${lowerNumber}`);
    await expect(page.locator('details.pull-stack')).toBeVisible();
    await expect(page.locator('#pull-stack-merge')).toHaveCount(0);

    await page.goto('/user2/repo1/pulls');
    const marks = (number: number) => page.locator('#issue-list .flex-item').filter({has: page.getByRole('link', {name: `#${number}`, exact: true})}).locator('.pull-stack-mark');
    await expect(marks(lowerNumber)).toHaveText('Bottom of a stack');
    await expect(marks(upperNumber)).toHaveText(`Stacked on #${lowerNumber}`);
  } finally {
    for (const number of [upperNumber, lowerNumber]) {
      await request.patch(`${api}/pulls/${number}`, {headers: adminAuth(), data: {state: 'closed'}});
    }
    for (const branch of [upper, lower]) {
      await request.delete(`${api}/branches/${branch}`, {headers: adminAuth()});
    }
  }
});
