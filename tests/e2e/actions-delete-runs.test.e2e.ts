// @watch start
// templates/repo/settings/actions.tmpl
// routers/web/repo/setting/actions.go
// services/actions/run_delete_old.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

test.use({user: 'user2'});

// The runs are not deleted here: the other specs use them.
test('delete runs older than: preview and cancel', async ({page}) => {
  await page.goto('/user2/repo1/settings/actions/general');
  await screenshot(page, page.locator('.repo-setting-content'), 8);

  await page.locator('#older_than').fill('30');
  await page.getByRole('button', {name: 'Show runs to delete'}).click();
  await expect(page.getByRole('heading', {name: 'Delete runs older than 30 days'})).toBeVisible();
  await expect(page.getByText(/Matching runs that can be deleted: \d+/)).toBeVisible();
  await expect(page.getByRole('button', {name: 'Delete these runs'})).toBeVisible();
  await screenshot(page, page.locator('.repo-setting-content'), 8);

  await page.getByRole('link', {name: 'Cancel'}).click();
  await expect(page).toHaveURL(/\/settings\/actions\/general$/);
});

test('delete runs older than: nothing matches', async ({page}) => {
  await page.goto('/user2/test_workflows/settings/actions/runs/delete?older_than=30');
  await expect(page.getByText('No runs match.')).toBeVisible();
  await expect(page.getByRole('button', {name: 'Delete these runs'})).toBeHidden();
  await screenshot(page, page.locator('.repo-setting-content'), 8);
});

test('delete runs older than: an invalid number is refused', async ({page}) => {
  await page.goto('/user2/repo1/settings/actions/runs/delete?older_than=0');
  await expect(page).toHaveURL(/\/settings\/actions\/general$/);
  await expect(page.locator('.flash-error')).toContainText('The number of days must be a whole number from 1 to 36500.');
});
