// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/shared/owned_tokens.tmpl
// templates/org/settings/deploy_keys.tmpl
// templates/repo/settings/deploy_keys.tmpl
// routers/web/shared/user/owned_token.go
// @watch end

import {randomBytes} from 'node:crypto';
import {expect, test as base, type Page} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

test.use({user: 'user2'});

async function shot(page: Page, name: string) {
  if (process.env.OWNED_TOKEN_SHOTS) await page.screenshot({path: `${process.env.OWNED_TOKEN_SHOTS}/${base.info().project.name}-${name}.png`, fullPage: true});
}

test('create a repository token, see its value once, and revoke it', async ({page}) => {
  const name = `ci-${randomBytes(4).toString('hex')}`;
  await page.goto('/user2/repo1/settings/keys');
  await expect(page.locator('#access-tokens')).toContainText('Access tokens (HTTPS and API)');
  await shot(page, '1-repo-page');

  await page.locator('#access-tokens').getByRole('button', {name: 'Add access token'}).click();
  const form = page.locator('#add-access-token-panel form');
  await expect(form.getByLabel('Read: clone, download and read through the API')).toBeChecked();
  await expect(form.locator('#access-token-coverage')).toHaveText('This repository only');
  await form.getByLabel('Name').fill(name);
  await form.getByLabel('Write: also push, and change issues, pull requests and packages').check();
  await shot(page, '2-repo-form');
  await screenshot(page);

  await form.getByRole('button', {name: 'Create token'}).click();
  const created = page.locator('#new-access-token');
  await expect(created).toContainText(name);
  await expect(page.locator('#new-access-token-value')).toHaveText(/^[0-9a-f]{40}$/);
  await shot(page, '3-repo-created');

  // a name that is taken keeps the form and what was entered
  await page.locator('#access-tokens').getByRole('button', {name: 'Add access token'}).click();
  await form.getByLabel('Name').fill(name);
  await form.getByRole('button', {name: 'Create token'}).click();
  await expect(page.locator('#access-token-error')).toContainText(`A token named ${name} already exists here.`);
  await expect(form.getByLabel('Name')).toHaveValue(name);
  await shot(page, '4-repo-refused');

  await page.goto('/user2/repo1/settings/keys');
  const item = page.locator('#access-token-list .flex-item', {hasText: name});
  await expect(item).toContainText('Write');
  await item.getByRole('button', {name: 'Revoke'}).click();
  const modal = page.locator('#revoke-access-token');
  await expect(modal).toContainText(`Revoke ${name}? Anything using it loses access immediately.`);
  await shot(page, '5-repo-revoke');
  await modal.getByRole('button', {name: 'Revoke'}).click();
  await expect(page.locator('.flash-success')).toHaveText('Token revoked');
  await expect(page.locator('#access-token-list .flex-item', {hasText: name})).toHaveCount(0);
});

test('organization token for selected repositories', async ({page}) => {
  const name = `release-${randomBytes(4).toString('hex')}`;
  await page.goto('/org/org3/settings/keys');
  await page.locator('#access-tokens').getByRole('button', {name: 'Add access token'}).click();
  const form = page.locator('#add-access-token-panel form');
  await form.getByLabel('Name').fill(name);
  await form.getByLabel('Selected repositories').check();
  await form.locator('.ui.dropdown').click();
  await form.locator('.ui.dropdown .item', {hasText: 'repo3'}).click();
  await page.keyboard.press('Escape');
  await shot(page, '6-org-form');
  await form.getByRole('button', {name: 'Create token'}).click();
  await expect(page.locator('#new-access-token')).toContainText(name);
  await expect(page.locator('#access-token-list .flex-item', {hasText: name})).toContainText('org3/repo3');
  await shot(page, '7-org-created');

  await page.goto('/org3/repo3/settings/keys');
  await expect(page.locator('#covering-access-tokens')).toContainText(name);
  await shot(page, '8-repo-covering');
});
