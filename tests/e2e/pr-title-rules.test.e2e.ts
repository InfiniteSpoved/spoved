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

test.use({user: 'user2'});

test('PR title rules: settings and failed merge check', async ({page}) => {
  const settings = '/user2/repo1/settings/push_rules';
  const save = async () => {
    await page.getByRole('button', {name: 'Update settings'}).click();
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
    await page.getByRole('button', {name: 'Update settings'}).click();
    await expect(page.locator('.negative.message')).toContainText('Invalid push rule');

    // the merge box shows the failed check
    await page.goto('/user2/repo1/pulls/2');
    const failed = page.locator('.merge.box .pr-title-rule-error');
    await expect(failed).toContainText('Start the title with ZZZ-1');
    await screenshot(page);
  } finally {
    await page.goto(settings);
    await page.getByLabel('Title must match').fill('');
    await page.getByLabel('Title must not match').fill('');
    await page.getByLabel('Only for pull requests into these branches').fill('');
    await page.getByLabel('Message shown when the title is refused').fill('');
    await page.getByLabel('Check the title when merging instead of when saving it').uncheck();
    await save();
  }
});
