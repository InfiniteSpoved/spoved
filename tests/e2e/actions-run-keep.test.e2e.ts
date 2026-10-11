// @watch start
// templates/repo/actions/**
// web_src/js/components/RepoActionView.vue
// routers/web/repo/actions/view.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

test.use({user: 'user1'});

// Run 190 of user5/repo4 (fixtures) is a waiting run.
test('keep a run', async ({page}) => {
  await page.goto('/user5/repo4/actions/runs/190/jobs/0/attempt/1');
  const keptMarker = page.locator('.action-info-summary').getByText('Kept', {exact: true});
  await expect(keptMarker).toBeHidden();

  await page.getByRole('button', {name: 'Keep run'}).click();
  await expect(page.getByRole('button', {name: 'Stop keeping run'})).toBeVisible();
  await expect(keptMarker).toBeVisible();
  await screenshot(page, page.locator('.action-info-summary'), 8);

  await page.goto('/user5/repo4/actions');
  const kept = page.locator('.flex-item').filter({hasText: 'job output'}).getByText('Kept', {exact: true});
  await expect(kept).toBeVisible();

  await page.goto('/user5/repo4/actions/runs/190/jobs/0/attempt/1');
  await page.getByRole('button', {name: 'Stop keeping run'}).click();
  await expect(page.getByRole('button', {name: 'Keep run'})).toBeVisible();
  await expect(keptMarker).toBeHidden();
});

test('a repository without runs has no kept marker', async ({page}) => {
  await page.goto('/user2/test_workflows/actions');
  await expect(page.getByText('The workflow has no runs yet.')).toBeVisible();
  await expect(page.getByText('Kept', {exact: true})).toBeHidden();
  await screenshot(page);
});
