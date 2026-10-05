// @watch start
// web_src/js/components/ActionRunGraph.vue
// web_src/js/components/action-run-graph.js
// routers/web/repo/actions/view.go
// @watch end

import {expect} from '@playwright/test';
import {test} from './utils_e2e.ts';

// The repository is declared in declare_repos_test.go: build -> test (matrix a, b) -> deploy, with no runner.
test('job graph shows the dependency levels and links to the job', async ({page}) => {
  await page.goto('/user2/actions-graph-test/actions/runs/1');

  const graph = page.locator('.action-run-graph');
  await expect(graph.locator('.action-run-graph-column')).toHaveCount(3);
  await expect(graph.locator('.action-run-graph-column').nth(0)).toContainText('build');
  await expect(graph.locator('.action-run-graph-column').nth(1)).toContainText('test');
  await expect(graph.locator('.action-run-graph-column').nth(1)).toContainText('2 jobs: 2 blocked');
  await expect(graph.locator('.action-run-graph-column').nth(2)).toContainText('deploy');
  await expect(graph.locator('.action-run-graph-lines path')).toHaveCount(2);

  // the matrix box expands into links to its jobs
  await graph.getByRole('button', {name: 'test'}).click();
  await expect(graph.locator('.action-run-graph-children a')).toHaveCount(2);

  // collapsing hides the graph
  await graph.getByRole('button', {name: 'Job graph'}).click();
  await expect(graph.locator('.action-run-graph-canvas')).toBeHidden();
  await graph.getByRole('button', {name: 'Job graph'}).click();

  await graph.locator('.action-run-graph-column').nth(0).locator('a').click();
  await expect(page).toHaveURL(/\/actions\/runs\/1\/jobs\/0(\/attempt\/1)?$/);
});
