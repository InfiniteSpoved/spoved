// @watch start
// templates/admin/org_category/**
// templates/admin/navbar.tmpl
// templates/explore/search.tmpl
// templates/explore/user_list.tmpl
// templates/explore/users.tmpl
// templates/org/settings/options.tmpl
// routers/web/admin/org_categories.go
// routers/web/explore/org.go
// routers/web/org/setting.go
// models/organization/org_category.go
// models/user/search.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';
import {screenshot} from './shared/screenshots.ts';
import {FakeData, fakeName} from './shared/fake-data.ts';

test.use({user: 'user1'});

let created: string[] = [];

test.afterEach(async ({page}) => {
  // categories the test didn't delete itself
  for (const name of created) {
    await page.goto('/admin/org-categories');
    const edit = page.locator('table tbody tr').filter({hasText: name}).locator('a[href*="/admin/org-categories/"]');
    if (await edit.count() > 0) await page.request.post(`${await edit.getAttribute('href')}/delete`);
  }
  created = [];
});

test('organization categories', async ({page, request}) => {
  const fake = new FakeData(request);
  try {
    const org = await fake.createOrg();
    const name = fakeName('category');
    created.push(name);

    // create
    await page.goto('/admin/org-categories');
    await page.getByRole('link', {name: 'New category'}).click();
    await page.getByLabel('Name', {exact: true}).fill(name);
    await page.getByLabel('Description', {exact: true}).fill('Payment services');
    await page.locator('#color').fill('#22aa55');
    await page.getByLabel('Position', {exact: true}).fill('1');
    await page.getByRole('button', {name: 'New category'}).click();
    await expect(page).toHaveURL(/\/admin\/org-categories$/);
    const row = page.locator('table tbody tr').filter({hasText: name});
    await expect(row).toContainText('Payment services');
    await screenshot(page);

    // a duplicate name is refused
    await page.getByRole('link', {name: 'New category'}).click();
    await page.getByLabel('Name', {exact: true}).fill(name);
    await page.getByRole('button', {name: 'New category'}).click();
    await expect(page.locator('.ui.negative.message')).toContainText('already exists');

    // edit form
    await page.goto('/admin/org-categories');
    await row.getByRole('link', {name: 'Edit category'}).click();
    await expect(page.locator('#color')).toHaveValue('#22aa55');
    await screenshot(page);

    // the organization settings picker
    await page.goto(`/org/${org}/settings`);
    await page.locator('#category_id').selectOption({label: name});
    await page.getByRole('button', {name: 'Update settings'}).click();
    await expect(page.locator('.flash-success')).toBeVisible();
    await expect(page.locator('#category_id option:checked')).toHaveText(name);
    await screenshot(page, page.locator('#category_box'));

    // Explore groups by category and filters by it
    await page.goto('/explore/organizations');
    const header = page.locator('h3.org-category-header').filter({hasText: name});
    await expect(header).toBeVisible();
    await expect(page.locator('h3.org-category-header').last()).toHaveText('Other');
    // Category sits left of Sort on the same row
    const categoryBox = await page.locator('[commandfor="search-category-dropdown"]').boundingBox();
    const sortBox = await page.locator('[commandfor="search-sort-dropdown"]').boundingBox();
    expect(Math.abs(categoryBox.y - sortBox.y)).toBeLessThan(2);
    expect(categoryBox.x).toBeLessThan(sortBox.x);
    await screenshot(page);
    await page.getByRole('button', {name: 'Category'}).click();
    await page.locator('#search-category-dropdown').getByRole('link', {name}).click();
    await expect(page).toHaveURL(/category=\d+/);
    await expect(page.locator('[commandfor="search-category-dropdown"]')).toHaveText(`Category: ${name}`);
    await page.getByRole('button', {name: `Category: ${name}`}).click();
    await expect(page.locator('#search-category-dropdown a.active')).toHaveText(name);
    await page.keyboard.press('Escape');
    await expect(page.locator('h3.org-category-header')).toHaveCount(0);
    await expect(page.locator('.flex-item-title a')).toHaveText([org]);
    await screenshot(page);

    // deleting moves the organization to "Other"
    await page.goto('/admin/org-categories');
    await row.getByRole('link', {name: 'Edit category'}).click();
    await page.getByRole('button', {name: 'Delete category'}).click();
    await page.locator('#delete-org-category').getByRole('button', {name: 'Yes'}).click();
    await expect(page).toHaveURL(/\/admin\/org-categories$/);
    await expect(row).toHaveCount(0);
    await page.goto(`/org/${org}/settings`);
    await expect(page.locator('#category_id option:checked', {hasText: name})).toHaveCount(0);
  } finally {
    await fake.cleanup();
  }
});
