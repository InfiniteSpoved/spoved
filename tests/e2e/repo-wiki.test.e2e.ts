// @watch start
// templates/repo/wiki/**
// templates/repo/settings/units/wiki.tmpl
// web_src/css/repo**
// web_src/js/features/repo-wiki.js
// @watch end

import {expect} from '@playwright/test';
import {create_temp_user, dynamic_id, test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

for (const searchTerm of ['space', 'consectetur']) {
  for (const width of [null, 2560, 4000]) {
    test(`Search for '${searchTerm}' and test for no overflow ${width && `on ${width}-wide viewport` || ''}`, async ({page, viewport}) => {
      await page.setViewportSize({
        width: width ?? viewport.width,
        height: 1440, // We're testing that we fit horizontally - vertical scrolling is fine.
      });
      await page.goto('/user2/repo1/wiki');
      await page.getByPlaceholder('Search wiki').fill(searchTerm);
      await page.getByPlaceholder('Search wiki').click();
      // workaround: HTMX listens on keyup events, playwright's fill only triggers the input event
      // so we manually "type" the last letter
      await page.getByPlaceholder('Search wiki').dispatchEvent('keyup');

      await expect(page.locator('#wiki-search a[href]')).toBeInViewport({
        ratio: 1,
      });
      await screenshot(page);
    });
  }
}

test(`Search results show titles (and not file names)`, async ({page}) => {
  await page.goto('/user2/repo1/wiki');
  await page.getByPlaceholder('Search wiki').fill('spaces');
  await page.getByPlaceholder('Search wiki').click();
  // workaround: HTMX listens on keyup events, playwright's fill only triggers the input event
  // so we manually "type" the last letter
  await page.getByPlaceholder('Search wiki').dispatchEvent('keyup');
  await expect(page.locator('#wiki-search a[href] b')).toHaveText('Page With Spaced Name');
  await screenshot(page);
});

test('Wiki unicode-escape', async ({page}) => {
  await page.goto('/user2/unicode-escaping/wiki');
  await screenshot(page);

  await expect(page.locator('.ui.message.unicode-escape-prompt')).toHaveCount(3);

  const unescapedElements = page.locator('.ambiguous-code-point');
  for (let i = 0; i < await unescapedElements.count(); i++) {
    // the border color is the text color (currentColor), whatever the theme makes it
    expect(await unescapedElements.nth(i).evaluate((el) => `0px solid ${getComputedStyle(el).color}` === getComputedStyle(el).border)).toBe(true);
  }

  await page.locator('a.escape-button').click();

  // the browser-resolved --color-yellow, so the expected value follows the theme
  const yellow = await page.locator('html').evaluate((el) => {
    const probe = document.createElement('div');
    probe.style.color = 'var(--color-yellow)';
    el.append(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    return color;
  });
  const escapedElements = page.locator('.ambiguous-code-point');
  for (let i = 0; i < await escapedElements.count(); i++) {
    expect(await escapedElements.nth(i).evaluate((el) => getComputedStyle(el).border)).toEqual(`1px solid ${yellow}`);
  }
});

test.describe('Wiki editing', () => {
  test.use({user: 'user2'});
  const wikiTitle = dynamic_id();

  test('New page', async ({page}) => {
    await page.goto('/user2/repo1/wiki?action=_new');

    await page.getByPlaceholder('Page title').fill(wikiTitle);
    await page.getByPlaceholder('Page content').fill('Welcome to the new wiki!');
    await page.getByText('Save page').click();

    await expect(page.locator('.file-content')).toContainText(wikiTitle);
    await expect(page.locator('.wiki-content-main')).toHaveText('Welcome to the new wiki!');
  });

  test('Edit page', async ({page}) => {
    await page.goto(`/user2/repo1/wiki/${wikiTitle}.-`);
    await page.getByRole('link', {name: 'Edit'}).click();

    await page.getByPlaceholder('Page content').fill('Wiki has been deprecated');
    await page.getByText('Save page').click();

    await expect(page.locator('.wiki-content-main')).toHaveText('Wiki has been deprecated');
  });
});

test.describe('Wiki from a repository folder', () => {
  // a fresh user: without this, new contexts inherit the user2 session of the spec
  test.use({user: null, storageState: {cookies: [], origins: []}});

  test('Set the folder and open the wiki', async ({browser, request}, workerInfo) => {
    const {context, username} = await create_temp_user(browser, workerInfo, request);
    const page = await context.newPage();
    const headers = {Authorization: `Basic ${btoa(`${username}:password`)}`, 'Content-Type': 'application/json'};
    const repo = dynamic_id();
    const created = await request.post('/api/v1/user/repos', {headers, data: {name: repo, auto_init: true}});
    expect(created.ok()).toBeTruthy();
    const branch = (await created.json()).default_branch;
    for (const [file, content] of [['docs/README.md', '# Welcome\n\nSee the [install guide](guide/install.md).\n'], ['docs/guide/install.md', '# Install\n']]) {
      expect((await request.post(`/api/v1/repos/${username}/${repo}/contents/${file}`, {headers, data: {message: `add ${file}`, content: btoa(content)}})).ok()).toBeTruthy();
    }

    await page.goto(`/${username}/${repo}/settings/units`);
    await page.getByLabel('Pages from a folder of this repository').check();
    await page.getByLabel('Folder', {exact: true}).fill('docs');
    await page.getByRole('button', {name: 'Save settings'}).first().click();
    await expect(page.getByLabel('Pages from a folder of this repository')).toBeChecked();

    await page.goto(`/${username}/${repo}/wiki`);
    await expect(page.locator('.wiki-content-main h1')).toHaveText('Welcome');
    await expect(page.locator('.wiki-folder-pages a')).toHaveText(['README', 'install']);
    await expect(page.getByRole('link', {name: 'Edit this page'})).toHaveAttribute('href', `/${username}/${repo}/_edit/${branch}/docs/README.md`);
    await screenshot(page);

    await page.locator('.wiki-folder-pages a', {hasText: 'install'}).click();
    await expect(page.locator('.wiki-content-main h1')).toHaveText('Install');
  });
});
