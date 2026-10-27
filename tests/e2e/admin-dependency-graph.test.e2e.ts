// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/admin/dependency_graph.tmpl
// templates/admin/dependency_graph_limit.tmpl
// routers/web/admin/dependency_graph.go
// web_src/js/features/admin/dependency-graph.js
// web_src/css/repo/dependencies.css
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {FakeData, fakeName, loginAs, adminAuth} from './shared/fake-data.ts';

test.describe('as the site admin', () => {
  test.use({user: 'user1'});

  test('Dependency graph settings are saved on the page', async ({page}) => {
    const response = await page.goto('/admin/config/dependency-graph');
    expect(response?.status()).toBe(200);
    const scans = page.locator('#dg-max_concurrent_scans');
    await expect(scans).toHaveValue('1');

    // the mirror fields show only with the mirror source
    await page.locator('label[for="dg-source-mirror"]').click();
    await expect(page.locator('#dg-mirror_url')).toBeVisible();
    await page.locator('label[for="dg-source-public"]').click();
    await expect(page.locator('#dg-mirror_url')).toBeHidden();

    await scans.fill('2');
    await page.locator('#dependency-graph-save').click();
    await expect(page.locator('#flash-message')).toContainText('The dependency graph settings were saved.');
    await expect(scans).toHaveValue('2');
    await expect(page.locator('#dg-max_concurrent_scans').locator('xpath=../..').locator('.dg-app-default')).toContainText('app.ini: 1.');

    // back to the app.ini value: no hint
    await scans.fill('1');
    await page.locator('#dependency-graph-save').click();
    await expect(page.locator('#flash-message')).toContainText('The dependency graph settings were saved.');
    await expect(scans).toHaveValue('1');
    await expect(page.locator('#dg-max_concurrent_scans').locator('xpath=../..').locator('.dg-app-default')).toHaveCount(0);
  });
});

test.describe('as an auditor', () => {
  test.use({user: null, storageState: {cookies: [], origins: []}});

  let data: FakeData;
  test.beforeEach(({request}) => {
    data = new FakeData(request);
  });
  test.afterEach(async () => {
    await data.cleanup();
  });

  test('The auditor reads the dependency graph settings and can not save them', async ({browser, request}) => {
    const auditor = await data.createUser();
    const group = fakeName('group');
    expect((await request.post('/api/v1/admin/groups', {headers: adminAuth(), data: {name: group, grants_auditor: true}})).status()).toBe(201);
    data.trackGroup(group);
    expect((await request.put(`/api/v1/admin/groups/${group}/members/${auditor.username}`, {headers: adminAuth()})).status()).toBe(204);

    const page = await (await loginAs(browser, auditor.username, auditor.password)).newPage();
    const response = await page.goto('/admin/config/dependency-graph');
    expect(response?.status()).toBe(200);
    await expect(page.locator('#dg-max_concurrent_scans')).toBeDisabled();
    await expect(page.locator('#dependency-graph-save')).toHaveCount(0);
    await expect(page.locator('#dependency-graph-test')).toHaveCount(0);
    const post = await page.request.post('/admin/config/dependency-graph/settings', {form: {source: 'public', max_concurrent_scans: '5'}, maxRedirects: 0});
    expect(post.status()).toBe(403);
  });
});
