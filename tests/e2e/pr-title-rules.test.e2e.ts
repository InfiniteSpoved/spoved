// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/repo/settings/push_rules.tmpl
// templates/repo/issue/view_content/pull.tmpl
// web_src/css/{form,repo}.css
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';
import {validate_form} from './shared/forms.ts';
import {adminAuth} from './shared/fake-data.ts';

test.use({user: 'user2'});

test('PR title rules: settings and failed merge check', async ({page, request}) => {
  // a real pull request, the fixtures' ones have no usable branches
  const branch = `title-rule-${Date.now()}`;
  const api = '/api/v1/repos/user2/repo1';
  expect((await request.post(`${api}/contents/${branch}.txt`, {headers: adminAuth(), data: {content: btoa(branch), new_branch: branch}})).ok()).toBeTruthy();
  const created = await request.post(`${api}/pulls`, {headers: adminAuth(), data: {head: branch, base: 'master', title: 'no ticket key'}});
  expect(created.ok()).toBeTruthy();
  const {number} = await created.json();

  const settings = '/user2/repo1/settings/push_rules';
  const save = async () => {
    await page.getByRole('button', {name: 'Save settings'}).click();
    await expect(page.locator('.flash-success')).toBeVisible();
  };

  try {
    await page.goto(settings);
    await validate_form({page}, 'fieldset');
    await page.getByLabel('Title must match').fill('^ZZZ-\\d+ ');
    await page.getByLabel('Message shown when the title is refused').fill('Start the title with ZZZ-1');
    await page.getByLabel('Only for pull requests into these branches').fill('master');
    await page.getByLabel('Check the title when merging instead of when saving it').check();
    await screenshot(page);
    await save();
    await expect(page.getByLabel('Title must match')).toHaveValue('^ZZZ-\\d+ ');
    await expect(page.getByLabel('Check the title when merging instead of when saving it')).toBeChecked();

    // an invalid expression is rejected and not stored
    await page.getByLabel('Title must not match').fill('(');
    await page.getByRole('button', {name: 'Save settings'}).click();
    await expect(page.locator('.negative.message')).toContainText('Invalid push rule');

    // the merge box shows the failed check
    await page.goto(`/user2/repo1/pulls/${number}`);
    const failed = page.locator('.merge.box .pr-title-rule-error');
    await expect(failed).toContainText('Start the title with ZZZ-1');
    await screenshot(page);
  } finally {
    await request.patch(`${api}/pulls/${number}`, {headers: adminAuth(), data: {state: 'closed'}});
    await request.delete(`${api}/branches/${branch}`, {headers: adminAuth()});
    await page.goto(settings);
    await page.getByLabel('Title must match').fill('');
    await page.getByLabel('Title must not match').fill('');
    await page.getByLabel('Only for pull requests into these branches').fill('');
    await page.getByLabel('Message shown when the title is refused').fill('');
    await page.getByLabel('Check the title when merging instead of when saving it').uncheck();
    await save();
  }
});
