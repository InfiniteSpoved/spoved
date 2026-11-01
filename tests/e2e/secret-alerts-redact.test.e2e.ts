// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/repo/security/secret.tmpl
// routers/web/repo/secret_alerts.go
// services/secretscan/redact.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';

// each browser project has its own issue and alert (see seedSecretRedact): a redaction can't be undone
const alertOf: Record<string, number> = {'chromium': 1, 'firefox': 2, 'Mobile Chrome': 3};

test.describe('the owner of a repository', () => {
  test.use({user: 'user11'});

  test('removes a secret from the text of an issue', async ({page}, testInfo) => {
    const n = alertOf[testInfo.project.name];
    await page.goto(`/user11/secret-redact-test/security/secrets/${n}`);
    const places = page.locator('.secret-locations');
    const redact = places.locator('button.secret-redact');
    await expect(redact).toHaveCount(2);

    // the dialog says what happens, and Cancel leaves everything as it is
    const dialog = page.locator('#secret-redact-modal');
    await redact.first().click();
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('#secret-redact-where')).toHaveText(`Issue #${n} · description`);
    await expect(dialog).toContainText('[secret removed]');
    await expect(dialog).toContainText('edit history');
    await expect(dialog).toContainText("can't be undone");
    await expect(dialog).toContainText('revoke it as well');
    await screenshot(page);
    await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
    await expect(dialog).toBeHidden();

    // confirmed: the text is redacted, the place is marked and its button is gone
    await redact.first().click();
    await dialog.getByRole('button', {name: 'Remove the secret', exact: true}).click();
    await expect(page.locator('.flash-success')).toContainText('The secret was removed from the text');
    await expect(places.locator('button.secret-redact')).toHaveCount(1);
    await expect(places).toContainText('Removed from the text');
    await expect(page.locator('#secret-activity')).toContainText('removed the secret from the text');
    await screenshot(page);

    // the comment's text no longer holds the secret: refused, nothing changes
    await places.locator('button.secret-redact').click();
    await dialog.getByRole('button', {name: 'Remove the secret', exact: true}).click();
    await expect(page.locator('.flash-error')).toContainText('The secret is no longer in this text');
    await screenshot(page);

    await page.goto(`/user11/secret-redact-test/issues/${n}`);
    await expect(page.locator('.comment-list .render-content').first()).toContainText('[secret removed]');
    await expect(page.locator('.comment-list').first()).not.toContainText('ghp_');
  });
});
