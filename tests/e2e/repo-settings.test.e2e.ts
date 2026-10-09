// @watch start
// templates/webhook/shared-settings.tmpl
// templates/repo/settings/**
// web_src/css/{form,repo}.css
// web_src/css/modules/grid.css
// web_src/js/features/comp/WebHookEditor.js
// @watch end

import {expect} from '@playwright/test';
import {test, dynamic_id} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';
import {validate_form} from './shared/forms.ts';
import {adminAuth, FakeData, loginAs} from './shared/fake-data.ts';

test.use({user: 'user2'});

test('repo webhook settings', async ({page}) => {
  const response = await page.goto('/user2/repo1/settings/hooks/forgejo/new');
  expect(response?.status()).toBe(200);

  await page.locator('input[name="events"][value="choose_events"]').click();
  await expect(page.locator('.hide-unless-checked')).toBeVisible();

  // check accessibility including the custom events (now visible) part
  await validate_form({page}, 'fieldset');
  await screenshot(page);

  await page.locator('input[name="events"][value="push_only"]').click();
  await expect(page.locator('.hide-unless-checked')).toBeHidden();
  await page.locator('input[name="events"][value="send_everything"]').click();
  await expect(page.locator('.hide-unless-checked')).toBeHidden();
  await screenshot(page);
});

test.describe('repo branch protection settings', () => {
  test.afterEach(async ({page}) => {
    // delete the rule for the next test
    await page.goto('/user2/repo1/settings/branches/');
    await page.waitForLoadState('domcontentloaded');
    const deleteButton = page.locator('.delete-button').first();
    test.skip(await deleteButton.isHidden(), 'Nothing to delete at this time');
    await deleteButton.click();
    await page.locator('#delete-protected-branch .actions .ok').click();
    // Here page.waitForLoadState('domcontentloaded') does not work reliably.
    // Instead, wait for the delete button to disappear.
    await expect(deleteButton).toHaveCount(0);
  });

  test('form', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/branches/edit');
    expect(response?.status()).toBe(200);

    await validate_form({page}, 'fieldset');

    // verify header is new
    await expect(page.locator('h4')).toContainText('new');
    await page.locator('input[name="rule_name"]').fill('testrule');
    await screenshot(page);
    await page.locator('button:text("Save rule")').click();
    // verify header is in edit mode
    await page.waitForLoadState('domcontentloaded');
    await screenshot(page);

    // find the edit button and click it
    const editButton = page.locator('a[href="/user2/repo1/settings/branches/edit?rule_name=testrule"]');
    await editButton.click();

    await page.waitForLoadState();
    await expect(page.locator('.repo-setting-content .header')).toContainText('Protection rules for branch', {ignoreCase: true, useInnerText: true});
    await screenshot(page);
  });
});

test.describe('deploy key pickers', () => {
  test('branch protection', async ({page}) => {
    const ruleName = `deploy-keys-${dynamic_id()}`;
    const response = await page.goto('/user2/repo1/settings/branches/edit');
    expect(response?.status()).toBe(200);

    await page.locator('input[name="rule_name"]').fill(ruleName);
    await page.locator('input[name="enable_push"][value="whitelist"]').click();

    const picker = page.locator('#whitelist_deploy_key_ids');
    await picker.click();
    // only write deploy keys are offered
    await expect(picker.locator('.menu .item', {hasText: 'e2e-read-key'})).toHaveCount(0);
    await picker.locator('.menu .item', {hasText: 'e2e-write-key'}).click();
    await expect(picker.locator('input[name="whitelist_deploy_key_ids"]')).toHaveValue('1001');
    await screenshot(page);

    await page.locator('button:text("Save rule")').click();
    await page.waitForLoadState('domcontentloaded');

    await page.goto(`/user2/repo1/settings/branches/edit?rule_name=${ruleName}`);
    await expect(picker.locator('input[name="whitelist_deploy_key_ids"]')).toHaveValue('1001');
    await expect(picker.locator('a.label', {hasText: 'e2e-write-key'})).toBeVisible();

    // clean up
    await page.goto('/user2/repo1/settings/branches/');
    const deleteButton = page.locator('.flex-item', {hasText: ruleName}).locator('.delete-button');
    await deleteButton.click();
    await page.locator('#delete-protected-branch .actions .ok').click();
    await expect(deleteButton).toHaveCount(0);
  });

  test('branch deletion and force push allow-lists', async ({page}) => {
    const ruleName = `delete-force-push-${dynamic_id()}`;
    const response = await page.goto('/user2/repo1/settings/branches/edit');
    expect(response?.status()).toBe(200);

    await page.locator('input[name="rule_name"]').fill(ruleName);
    for (const name of ['delete_allowlist', 'force_push_allowlist']) {
      await page.locator(`#${name}_users`).click();
      await page.locator(`#${name}_users .menu .item[data-value="2"]`).click();
      const keys = page.locator(`#${name}_deploy_key_ids`);
      await keys.click();
      // only write deploy keys are offered
      await expect(keys.locator('.menu .item', {hasText: 'e2e-read-key'})).toHaveCount(0);
      await keys.locator('.menu .item', {hasText: 'e2e-write-key'}).click();
    }
    await screenshot(page);

    await page.locator('button:text("Save rule")').click();
    await page.waitForLoadState('domcontentloaded');

    await page.goto(`/user2/repo1/settings/branches/edit?rule_name=${ruleName}`);
    for (const name of ['delete_allowlist', 'force_push_allowlist']) {
      await expect(page.locator(`input[name="${name}_users"]`)).toHaveValue('2');
      await expect(page.locator(`input[name="${name}_deploy_key_ids"]`)).toHaveValue('1001');
    }

    // clean up
    await page.goto('/user2/repo1/settings/branches/');
    const deleteButton = page.locator('.flex-item', {hasText: ruleName}).locator('.delete-button');
    await deleteButton.click();
    await page.locator('#delete-protected-branch .actions .ok').click();
    await expect(deleteButton).toHaveCount(0);
  });

  test('tag protection', async ({page}) => {
    const pattern = `deploy-keys-${dynamic_id()}-*`;
    const response = await page.goto('/user2/repo1/settings/tags');
    expect(response?.status()).toBe(200);

    await page.locator('input[name="name_pattern"]').fill(pattern);
    const picker = page.locator('#allowlist_deploy_key_ids');
    await picker.click();
    await expect(picker.locator('.menu .item', {hasText: 'e2e-read-key'})).toHaveCount(0);
    await picker.locator('.menu .item', {hasText: 'e2e-write-key'}).click();
    await screenshot(page);
    await page.getByRole('button', {name: 'Add rule'}).click();
    await page.waitForLoadState('domcontentloaded');

    const row = page.locator('tr', {hasText: pattern});
    await expect(row.locator('.label', {hasText: 'e2e-write-key'})).toBeVisible();

    // clean up
    await row.getByRole('button', {name: 'Remove'}).click();
    await expect(row).toHaveCount(0);
  });
});

test.describe('repo actions secrets settings', () => {
  const secretName = dynamic_id().replaceAll('-', '_').toUpperCase();

  test('create secret', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/actions/secrets');
    expect(response?.status()).toBe(200);

    await page.getByText('Add secret').click();
    const modal = page.locator('#add-secret-modal');
    await expect(modal).toBeVisible();

    await modal.getByLabel('Name').fill(secretName);
    await modal.getByLabel('Value').fill('All Right Then, Keep Your Secrets');
    await modal.getByText('Confirm').click();

    await expect(page.getByText(secretName, {exact: true})).toBeVisible();
  });

  test('delete secret', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/actions/secrets');
    expect(response?.status()).toBe(200);

    const deleteButton = page.locator('.flex-item').filter({hasText: secretName}).getByLabel('Remove secret');
    await deleteButton.click();
    const modal = page.locator('#delete-secret');
    await expect(modal).toBeVisible();

    await modal.getByText('Confirm').click();
    await expect(page.getByText('There are no secrets yet.')).toBeVisible();
  });
});

test.describe('repo actions variables settings', () => {
  const variableName = dynamic_id().replaceAll('-', '_').toUpperCase();

  test('create variable', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/actions/variables');
    expect(response?.status()).toBe(200);

    await page.getByText('Add variable').click();
    const modal = page.locator('#edit-variable-modal');
    await expect(modal).toBeVisible();

    await modal.getByLabel('Name').fill(variableName);
    await modal.getByLabel('Value').fill("Will Frogejo cease to exist if there's a Forgejo trademark?");
    await modal.getByText('Confirm').click();

    await expect(page.getByText(variableName, {exact: true})).toBeVisible();
  });

  test('delete variable', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/actions/variables');
    expect(response?.status()).toBe(200);

    const deleteButton = page.locator('.flex-item').filter({hasText: variableName}).getByLabel('Remove variable');
    await deleteButton.click();
    const modal = page.locator('#delete-variable');
    await expect(modal).toBeVisible();

    await modal.getByText('Confirm').click();
    await expect(page.getByText('There are no variables yet.')).toBeVisible();
  });
});

test.describe('repo collaboration settings', () => {
  test('add collaborator', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/collaboration');
    expect(response?.status()).toBe(200);

    await page.getByPlaceholder('Search users…').fill('user5');
    await page.getByRole('button', {name: 'Add collaborator'}).click();

    await expect(page.getByRole('link', {name: 'user5 (User Five)'})).toBeVisible();
  });

  test('change access mode', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/collaboration');
    expect(response?.status()).toBe(200);

    const opener = page.locator('.access-mode .opener');
    await expect(opener).toHaveText('Write');

    // Check that opener and rmButton are same height
    const rmButton = page.getByRole('button', {name: 'Remove'});
    expect((await opener.boundingBox()).height).toBe((await rmButton.boundingBox()).height);

    // Open dropdown and change access mode to Read
    await opener.click();
    await page.getByRole('button', {name: 'Read'}).click();

    await expect(opener).toHaveText('Read');
  });

  test('remove collaborator', async ({page}) => {
    const response = await page.goto('/user2/repo1/settings/collaboration');
    expect(response?.status()).toBe(200);

    await page.getByRole('button', {name: 'Remove'}).click();
    const modal = page.locator('#delete-collaborator');
    await expect(modal).toBeVisible();
    await modal.getByRole('button', {name: 'Yes'}).click();

    await expect(page.getByText('There are no collaborators yet.')).toBeVisible();
  });
});

test.describe('as fake users', () => {
  // a clean state, so that loginAs doesn't inherit user2's session
  test.use({user: null, storageState: {cookies: [], origins: []}});

  test('repo visibility follows the organization policy', async ({browser, request}) => {
    const data = new FakeData(request);
    try {
      const member = await data.createUser();
      const org = await data.createOrg();
      const repo = await data.createRepo(org);
      const team = await data.createTeam(org, 'admin', [repo]);
      expect((await request.put(`/api/v1/teams/${team.id}/members/${member.username}`, {headers: adminAuth()})).ok()).toBeTruthy();
      expect((await request.patch(`/api/v1/orgs/${org}`, {headers: adminAuth(), data: {restrict_repo_visibility_change: true}})).ok()).toBeTruthy();

      const page = await (await loginAs(browser, member.username, member.password)).newPage();
      await page.goto(`/${org}/${repo}/settings`);
      await expect(page.locator('input[name="private"][type="checkbox"]')).toBeDisabled();
      await expect(page.locator('.org-policy-note')).toContainText('Blocked by the organization policy "Only organization owners can change the visibility of repositories".');
      await screenshot(page);

      // other settings can still be saved, and the repository stays private
      await page.locator('#description').fill('policy');
      await page.getByRole('button', {name: 'Save settings'}).first().click();
      await expect(page.locator('.flash-success')).toBeVisible();
      await expect(page.locator('input[name="private"][type="checkbox"]')).toBeChecked();
    } finally {
      await data.cleanup();
    }
  });
});
