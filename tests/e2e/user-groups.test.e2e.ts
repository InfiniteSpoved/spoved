// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// @watch start
// templates/admin/user_group/**
// templates/org/team/members.tmpl
// routers/web/admin/user_groups.go
// routers/web/org/teams.go
// services/usergroup/**
// @watch end

import {expect} from '@playwright/test';
import {test, create_temp_user, dynamic_id} from './utils_e2e.ts';

test.use({user: 'user1'});

test('User group shared by teams of several organizations', async ({browser, page, request}, workerInfo) => {
  const {context: memberContext, username} = await create_temp_user(browser, workerInfo, request);
  const memberPage = await memberContext.newPage();
  const groupName = dynamic_id();

  // org3/repo3 is private: the new user cannot see it yet
  expect((await memberPage.goto('/org3/repo3'))?.status()).toBe(404);

  // create the group
  await page.goto('/admin/groups');
  await page.getByRole('link', {name: 'Create user group'}).click();
  await page.getByLabel('Group name').fill(groupName);
  await page.getByLabel('Description').fill('Shared release managers');
  await page.getByRole('button', {name: 'Create user group'}).click();
  await expect(page.locator('.flash-success')).toContainText(`User group "${groupName}" has been created.`);
  const groupURL = page.url();

  // add the user to the group
  await page.getByPlaceholder('Search users…').fill(username);
  await page.getByRole('button', {name: 'Add member'}).click();
  await expect(page.locator('.flash-success')).toContainText(`${username} has been added to the group.`);
  await expect(page.locator('#user-group-members + .segment + .segment')).toContainText(username);

  // link the group to a team in two different organizations
  for (const teamURL of ['/org/org3/teams/team1', '/org/org17/teams/test_team']) {
    await page.goto(teamURL);
    // type part of the name and pick the group from the suggestions
    await page.getByPlaceholder('User group name').fill(groupName.slice(0, 20));
    await page.locator('#search-user-group-box .results .result', {hasText: groupName}).click();
    await expect(page.getByPlaceholder('User group name')).toHaveValue(groupName);
    await page.getByRole('button', {name: 'Link user group'}).click();
    await expect(page.locator('#team-user-groups + .segment + .segment')).toContainText(groupName);
    await expect(page.locator('.flex-item', {hasText: username})).toContainText('joined via a user group');
  }

  // the member got the access of the team
  expect((await memberPage.goto('/org3/repo3'))?.status()).toBe(200);

  // the group page lists both teams
  await page.goto(groupURL);
  const linkedTeams = page.locator('#user-group-teams + .segment');
  await expect(linkedTeams.getByRole('link', {name: 'team1'})).toBeVisible();
  await expect(linkedTeams.getByRole('link', {name: 'test_team'})).toBeVisible();

  // unlinking from org3 revokes access there
  await page.goto('/org/org3/teams/team1');
  await page.locator('#team-user-groups + .segment + .segment .flex-item', {hasText: groupName}).getByRole('button', {name: 'Unlink'}).click();
  await expect(page.locator('#team-user-groups + .segment + .segment')).not.toContainText(groupName);
  await expect(page.locator('.flex-item', {hasText: username})).toHaveCount(0);
  expect((await memberPage.goto('/org3/repo3'))?.status()).toBe(404);

  // deleting the group cleans up the remaining team
  await page.goto(groupURL);
  await page.getByRole('button', {name: 'Delete user group'}).click();
  await page.locator('#delete-user-group').getByRole('button', {name: 'Yes'}).click();
  await expect(page).toHaveURL(/\/admin\/groups$/);
  await expect(page.locator('.flash-success')).toContainText(`User group "${groupName}" has been deleted.`);
  await page.goto('/org/org17/teams/test_team');
  await expect(page.locator('.flex-item', {hasText: username})).toHaveCount(0);
});
