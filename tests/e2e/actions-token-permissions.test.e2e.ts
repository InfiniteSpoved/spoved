// @watch start
// web_src/js/components/RepoActionView.vue
// web_src/js/components/ActionJobStepList.vue
// routers/web/repo/actions/view.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

// Run 187 of user5/repo4 (fixtures) has a running task whose workflow has no `permissions:` block.
test('job view shows the token permissions', async ({page}) => {
  await page.goto('/user5/repo4/actions/runs/187/jobs/0/attempt/3');

  const permissions = page.locator('.job-token-permissions');
  await expect(permissions).toBeVisible();
  // collapsed by default, so that the logs stay where they were
  await expect(permissions.locator('.job-step-logs')).toBeHidden();
  await permissions.getByText('Token permissions').click();

  const rows = permissions.locator('.job-token-permission');
  await expect(rows).toHaveCount(6);
  await expect(rows.first()).toHaveText(/Code\s*Write/);
  await expect(permissions).toContainText('The workflow has no permissions block, so the token can write to every unit.');
  await screenshot(page, permissions, 8);
});
