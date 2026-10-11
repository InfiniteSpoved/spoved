// @watch start
// templates/repo/settings/actions.tmpl
// templates/repo/settings/navbar.tmpl
// routers/web/repo/setting/actions.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';
import {validate_form} from './shared/forms.ts';

test.use({user: 'user2'});

test('repo default job token permissions', async ({page}) => {
  const response = await page.goto('/user2/repo1/settings/actions/general');
  expect(response?.status()).toBe(200);

  const instance = page.locator('#default_token_permissions_instance');
  const read = page.locator('#default_token_permissions_read');
  await expect(instance).toBeChecked();
  await validate_form({page}, 'form');
  await screenshot(page);

  await page.getByText('Read-only', {exact: true}).click();
  await page.getByRole('button', {name: 'Save settings'}).click();
  await expect(page.locator('.flash-success')).toBeVisible();
  await expect(read).toBeChecked();
  await page.reload();
  await expect(read).toBeChecked();

  // back to the instance default for the other tests
  await page.getByText('Use the instance default', {exact: true}).click();
  await page.getByRole('button', {name: 'Save settings'}).click();
  await expect(instance).toBeChecked();
});
