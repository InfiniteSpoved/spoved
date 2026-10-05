import {mount, flushPromises} from '@vue/test-utils';
import ActionRunGraph from './ActionRunGraph.vue';

const locale = {
  graphTitle: 'Job graph',
  graphMatrixCount: '%[1]d jobs',
  status: {success: 'Success', failure: 'Failure', waiting: 'Waiting', blocked: 'Blocked', running: 'Running'},
};
const job = (id, name, groupKey, needs = [], status = 'success') => ({id, name, groupKey, needs, status, duration: '1s'});
const mountGraph = (jobs) => mount(ActionRunGraph, {props: {jobs, runLink: '/o/r/actions/runs/1', currentJobIndex: 0, locale}});
const pipeline = () => [
  job(1, 'build', '/build'),
  job(2, 'test (a)', '/test', [1]),
  job(3, 'test (b)', '/test', [1], 'failure'),
  job(4, 'deploy', '/deploy', [2, 3], 'blocked'),
];

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

// criterion 1, 2, 3 (name, status, duration)
test('boxes in dependency columns with arrows, status and duration', async () => {
  const wrapper = mountGraph(pipeline());
  await flushPromises();
  expect(wrapper.findAll('.action-run-graph-column')).toHaveLength(3);
  expect(wrapper.findAll('.action-run-graph-lines path')).toHaveLength(2);
  const deploy = wrapper.findAll('.action-run-graph-column')[2];
  expect(deploy.text()).toContain('deploy');
  expect(deploy.text()).toContain('1s');
  expect(deploy.find('[data-tooltip-content="Blocked"]').exists()).toBe(true);
});

// criterion 3: statuses update live
test('re-renders when the jobs change', async () => {
  const wrapper = mountGraph(pipeline());
  expect(wrapper.findAll('.action-run-graph-column')[0].find('[data-tooltip-content="Success"]').exists()).toBe(true);
  await wrapper.setProps({jobs: [job(1, 'build', '/build', [], 'running'), ...pipeline().slice(1)]});
  expect(wrapper.findAll('.action-run-graph-column')[0].find('[data-tooltip-content="Running"]').exists()).toBe(true);
});

// criterion 4
test('matrix jobs share a box with per-status counts and expand to links', async () => {
  const wrapper = mountGraph(pipeline());
  const matrix = wrapper.findAll('.action-run-graph-column')[1];
  expect(matrix.text()).toContain('2 jobs: 1 failure, 1 success');
  expect(matrix.find('.action-run-graph-children').exists()).toBe(false);
  await matrix.get('button').trigger('click');
  const links = matrix.findAll('.action-run-graph-children a');
  expect(links.map((a) => a.attributes('href'))).toEqual(['/o/r/actions/runs/1/jobs/1', '/o/r/actions/runs/1/jobs/2']);
  expect(links.map((a) => a.text())).toEqual(['test (a)1s', 'test (b)1s']); // name and duration
});

test('the matrix count line counts every status', () => {
  const jobs = [job(1, 't (1)', '/t', [], 'failure'), job(2, 't (2)', '/t', [], 'running'), job(3, 't (3)', '/t', [], 'waiting'),
    job(4, 't (4)', '/t', [], 'waiting'), job(5, 't (5)', '/t', [], 'waiting'), job(6, 't (6)', '/t', [], 'running')];
  expect(mountGraph(jobs).get('.action-run-graph-count').text()).toBe('6 jobs: 1 failure, 2 running, 3 waiting');
});

// criterion 5
test('boxes link to the job and hovering highlights its dependencies', async () => {
  const wrapper = mountGraph(pipeline());
  await flushPromises();
  const [build, , deploy] = wrapper.findAll('.action-run-graph-node');
  expect(build.get('a').attributes('href')).toBe('/o/r/actions/runs/1/jobs/0');
  await deploy.trigger('mouseenter');
  expect(build.classes()).toContain('dim'); // build is not a direct dependency of deploy (test is)
  expect(wrapper.findAll('.action-run-graph-node')[1].classes()).not.toContain('dim');
  expect(wrapper.findAll('.action-run-graph-lines path.active')).toHaveLength(1);
  await deploy.trigger('mouseleave');
  expect(wrapper.findAll('.dim')).toHaveLength(0);
});

// criterion 7
test('collapsing is remembered in localStorage', async () => {
  const wrapper = mountGraph(pipeline());
  expect(wrapper.get('.action-run-graph-canvas').element.style.display).not.toBe('none');
  await wrapper.get('.action-run-graph-toggle').trigger('click');
  expect(wrapper.get('.action-run-graph-canvas').element.style.display).toBe('none');
  expect(localStorage.getItem('actions-run-graph-collapsed')).toBe('true');
  expect(mountGraph(pipeline()).get('.action-run-graph-canvas').element.style.display).toBe('none');
});

test('still works when localStorage throws', async () => {
  vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
    throw new Error('denied');
  });
  const wrapper = mountGraph(pipeline());
  await wrapper.get('.action-run-graph-toggle').trigger('click');
  expect(wrapper.get('.action-run-graph-canvas').element.style.display).toBe('none');
});

// criterion 8
test('job names are rendered as text, never as HTML', () => {
  const wrapper = mountGraph([job(1, '<b>x</b>', '/a'), job(2, '<img src=x>', '/b', [1])]);
  expect(wrapper.find('b').exists()).toBe(false);
  expect(wrapper.find('img').exists()).toBe(false);
  expect(wrapper.text()).toContain('<b>x</b>');
});
