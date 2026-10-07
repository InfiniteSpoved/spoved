// @watch start
// templates/repo/settings/protected_branch.tmpl
// routers/web/{repo,org}/setting/protected_branch.go
// web_src/js/features/repo-settings.js
// @watch end

import {expect, type Page} from '@playwright/test';
import {test, dynamic_id} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

test.use({user: 'user2'});

const forms = [
  {name: 'repo', base: '/user2/repo1/settings/branches', team: false},
  {name: 'org', base: '/org/org3/settings/branches', team: true},
];

for (const {name, base, team} of forms) {
  test.describe(`approval rules editor (${name})`, () => {
    const cards = (page: Page) => page.locator('.approval-rule-card');
    const add = (page: Page) => page.locator('.approval-rule-add');
    const save = (page: Page) => page.locator('button:text("Save rule")');

    async function deleteRule(page: Page, rule: string) {
      await page.goto(`${base}/`);
      const deleteButton = page.locator('.flex-item', {hasText: rule}).locator('.delete-button');
      await deleteButton.click();
      await page.locator('#delete-protected-branch .actions .ok').click();
      await expect(deleteButton).toHaveCount(0);
    }

    async function pickUser(page: Page, card: number, user: string) {
      const dropdown = cards(page).nth(card).locator('.ui.dropdown', {has: page.locator('input[name="approval_rule_users"]')});
      await dropdown.click();
      await dropdown.locator(`.menu .item[data-value="${user}"]`).click();
    }

    test('no blank rows, add, remove, save and reload', async ({page}) => {
      const rule = `ar-editor-${dynamic_id()}`;
      await page.goto(`${base}/edit`);
      await expect(cards(page)).toHaveCount(0);
      await expect(add(page)).toBeEnabled();
      await expect(page.locator('.approval-rule-limit')).toBeHidden();
      await screenshot(page);

      await page.locator('input[name="rule_name"]').fill(rule);
      for (const ruleName of ['First', 'Second']) {
        await add(page).click();
        await cards(page).last().locator('input[name="approval_rule_name"]').fill(ruleName);
        await pickUser(page, (await cards(page).count()) - 1, 'user2');
      }
      await expect(cards(page)).toHaveCount(2);
      // the card header follows the name
      await expect(cards(page).first().locator('.approval-rule-title')).toHaveText('First');
      if (team) await expect(cards(page).first().locator('input[name="approval_rule_teams"]')).toHaveCount(1);
      else await expect(page.locator('input[name="approval_rule_teams"]')).toHaveCount(0);
      await screenshot(page);

      await cards(page).first().locator('.approval-rule-remove').click();
      await expect(cards(page)).toHaveCount(1);

      await save(page).click();
      await page.waitForLoadState('domcontentloaded');
      await page.goto(`${base}/edit?rule_name=${rule}`);
      await expect(cards(page)).toHaveCount(1);
      await expect(cards(page).first().locator('input[name="approval_rule_name"]')).toHaveValue('Second');
      await expect(cards(page).first().locator('input[name="approval_rule_users"]')).toHaveValue('user2');
      await screenshot(page);

      // removing the last card saves "no rules"
      await cards(page).first().locator('.approval-rule-remove').click();
      await save(page).click();
      await page.waitForLoadState('domcontentloaded');
      await page.goto(`${base}/edit?rule_name=${rule}`);
      await expect(cards(page)).toHaveCount(0);

      await deleteRule(page, rule);
    });

    test('Add is disabled at the limit', async ({page}) => {
      await page.goto(`${base}/edit`);
      for (let i = 0; i < 20; i++) await add(page).click();
      await expect(cards(page)).toHaveCount(20);
      await expect(add(page)).toBeDisabled();
      await expect(page.locator('.approval-rule-limit')).toBeVisible();
      await expect(page.locator('.approval-rule-limit')).toContainText('At most 20 rules');
      await cards(page).last().locator('.approval-rule-remove').click();
      await expect(add(page)).toBeEnabled();
      await expect(page.locator('.approval-rule-limit')).toBeHidden();
    });

    test('half-filled card is not submitted, empty new card is ignored', async ({page}) => {
      const rule = `ar-editor-${dynamic_id()}`;
      await page.goto(`${base}/edit`);
      await page.locator('input[name="rule_name"]').fill(rule);
      await add(page).click();
      await cards(page).first().locator('input[name="approval_rule_name"]').fill('Half');

      // a name without users, teams or code owners
      await save(page).click();
      await expect(cards(page).first().locator('.approval-rule-error')).toBeVisible();
      await expect(cards(page).first().locator('.field.error').first()).toBeVisible();
      await expect(page).toHaveURL(`${base}/edit`);
      await screenshot(page);

      // an empty new card is ignored, the rule is saved without rules
      await cards(page).first().locator('input[name="approval_rule_name"]').fill('');
      await add(page).click();
      await expect(cards(page)).toHaveCount(2);
      await save(page).click();
      await page.waitForURL(/\/settings\/branches(\?|$)/);
      await page.goto(`${base}/edit?rule_name=${rule}`);
      await expect(cards(page)).toHaveCount(0);

      await deleteRule(page, rule);
    });
  });
}
