import {test, expect} from 'vitest';
import {layoutGraph} from './action-run-graph.js';

const job = (id, name, groupKey, needs = [], status = 'success') => ({id, name, groupKey, needs, status});

test('levels follow needs and matrix jobs share a node', () => {
  const {columns, edges} = layoutGraph([
    job(1, 'build', '/build'),
    job(2, 'test (a)', '/test', [1]),
    job(3, 'test (b)', '/test', [1], 'failure'),
    job(4, 'deploy', '/deploy', [2, 3]),
  ]);
  expect(columns.map((c) => c.map((n) => n.key))).toEqual([['/build'], ['/test'], ['/deploy']]);
  expect(columns[1][0]).toMatchObject({name: 'test', status: 'failure'});
  expect(columns[1][0].jobs.map((j) => j.index)).toEqual([1, 2]);
  expect(edges).toEqual([['/build', '/test'], ['/test', '/deploy']]);
});

test('unknown needs and cycles do not break the layout', () => {
  const {columns} = layoutGraph([job(1, 'a', '/a', [2, 99]), job(2, 'b', '/b', [1])]);
  expect(columns.flat()).toHaveLength(2);
});
