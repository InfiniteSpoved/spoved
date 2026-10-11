// @watch start
// templates/repo/settings/actions.tmpl
// templates/org/settings/actions.tmpl
// templates/org/settings/navbar.tmpl
// routers/web/repo/setting/actions.go
// routers/web/org/setting/actions_retention.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

test.use({user: 'user2'});

test('repository run retention', async ({page}) => {
  await page.goto('/user2/repo1/settings/actions/general');
  const days = page.locator('#run_retention_days');
  await expect(days).toHaveValue('');
  await expect(page.getByText('Runs of this repository are currently kept forever.')).toBeVisible();
  await screenshot(page, page.locator('.repo-setting-content'), 8);

  await days.fill('7');
  await days.press('Enter');
  await expect(page.getByText('The run retention was saved.')).toBeVisible();
  await expect(days).toHaveValue('7');
  await expect(page.getByText('Runs of this repository are currently deleted after 7 days.')).toBeVisible();

  await page.goto('/user2/repo1/actions');
  await expect(page.getByText('Runs older than 7 days are deleted automatically.')).toBeVisible();

  // back to no setting
  await page.goto('/user2/repo1/settings/actions/general');
  await days.fill('');
  await days.press('Enter');
  await expect(page.getByText('The run retention was saved.')).toBeVisible();
  await expect(days).toHaveValue('');
});

test('organization run retention', async ({page}) => {
  await page.goto('/org/org3/settings/actions/retention');
  const days = page.locator('#run_retention_days');
  await expect(days).toHaveValue('');
  await screenshot(page, page.locator('.org-setting-content'), 8);

  await days.fill('14');
  await days.press('Enter');
  await expect(page.getByText('The run retention was saved.')).toBeVisible();
  await expect(days).toHaveValue('14');

  await days.fill('');
  await days.press('Enter');
  await expect(page.getByText('The run retention was saved.')).toBeVisible();
  await expect(days).toHaveValue('');
});
