// @watch start
// tests/e2e/utils_e2e.ts
// web_src/js/features/repo-branch-tag-selector.js
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';

test.use({user: 'user2'});

const chunk = '**/assets/js/repo-branch-tag-selector.*.js';

async function loadRepoPage(page) {
  const chunkError = page.waitForEvent('pageerror');
  await page.goto('/user2/repo1');
  await chunkError;
  await new Promise((resolve) => setTimeout(resolve, 2000)); // the guard checks the error after 1 s
  expect(page.url()).toContain('/user2/repo1');
}

// Firefox aborts a lazy chunk's request when the page navigates away and webpack turns that into a
// ChunkLoadError; the guard in utils_e2e.ts must ignore it. Aborting the route stands in for it.
test('Aborted lazy chunk is not a page error', async ({page}) => {
  await page.route(chunk, (route) => route.abort('aborted'));
  await loadRepoPage(page);
});

// A chunk the real server answers with 404 must still fail the test.
test('Missing lazy chunk is a page error', async ({page}) => {
  test.fail(); // eslint-disable-line no-jquery/no-done-fail
  await page.route(chunk, (route) => route.continue({url: route.request().url().replace('.js', '-missing.js')}));
  await loadRepoPage(page);
});
