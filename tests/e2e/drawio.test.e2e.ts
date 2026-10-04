// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// modules/markup/drawio/**
// public/assets/vendor/drawio/**
// routers/web/repo/render.go
// @watch end

import {expect, type Request} from '@playwright/test';
import {test} from './utils_e2e.ts';

test('draw.io diagram renders in a sandboxed iframe', async ({page}) => {
  const foreign: Request[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).hostname !== 'localhost') foreign.push(req);
  });

  const response = await page.goto('/user2/drawio-test/src/branch/main/docs/arch.drawio');
  expect(response?.status()).toBe(200);

  const iframe = page.locator('iframe.external-render');
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts');
  await expect(page.frameLocator('iframe.external-render').getByText('Hello drawio')).toBeVisible();
  await expect.poll(async () => (await iframe.boundingBox())?.height).toBeGreaterThan(100);

  // the HTML label must not run its onerror handler
  const frame = page.frames().find((f) => f.url().includes('/render/'));
  expect(await frame?.evaluate(() => document.title)).not.toBe('pwned');
  // the CSP blocks fetches from outside Forgejo, e.g. the external image in the diagram.
  // Chromium reports them as failed with "csp", Firefox never starts them.
  for (const req of foreign) {
    await expect.poll(() => req.failure()?.errorText).toBe('csp');
  }
});

test('draw.io SVG renders as an image in Markdown', async ({page}) => {
  const response = await page.goto('/user2/drawio-test/src/branch/main/diagram.md');
  expect(response?.status()).toBe(200);

  const img = page.locator('.file-view.markup img[src$="arch.drawio.svg"]');
  await expect(img).toBeVisible();
  expect(await img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBe(120);
});
