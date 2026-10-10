// @watch start
// tests/e2e/utils_e2e.ts
// web_src/js/features/repo-branch-tag-selector.js
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';

test.use({user: 'user2'});

const chunk = '**/assets/js/repo-branch-tag-selector.*.js';

// A browser aborts a lazy chunk's request when the page navigates away (seen in Firefox) and webpack
// raises a ChunkLoadError; the guard in utils_e2e.ts must ignore it. Aborting the route stands in for it.
test('Aborted lazy chunk is not a page error', async ({page}) => {
  await page.route(chunk, (route) => route.abort('aborted'));
  const chunkError = page.waitForEvent('pageerror');
  await page.goto('/user2/repo1');
  expect((await chunkError).name).toBe('ChunkLoadError');
  await expect(page).toHaveURL(/\/user2\/repo1$/);
});

// A chunk the real server answers with 404 must still fail the test.
test('Missing lazy chunk is a page error', async ({page}) => {
  test.fail(); // eslint-disable-line no-jquery/no-done-fail
  await page.route(chunk, (route) => route.fulfill({status: 404, contentType: 'text/plain', body: 'not found'}));
  const chunkError = page.waitForEvent('pageerror');
  await page.goto('/user2/repo1');
  expect((await chunkError).name).toBe('ChunkLoadError');
  await expect(page).toHaveURL(/\/user2\/repo1$/);
});
