// Worst status first: a group shows the most urgent status of its jobs.
export const statusOrder = ['failure', 'running', 'waiting', 'blocked', 'cancelled', 'skipped', 'success'];

// Groups matrix jobs by groupKey and puts each group in a column one past the deepest group it needs.
// Returns {columns: node[][], edges: [fromKey, toKey][]}, a node being {key, name, status, jobs: [{...job, index}]}.
export function layoutGraph(jobs) {
  const nodes = new Map();
  const keyByID = new Map();
  for (const [index, job] of jobs.entries()) {
    keyByID.set(job.id, job.groupKey);
    if (!nodes.has(job.groupKey)) nodes.set(job.groupKey, {key: job.groupKey, jobs: [], needs: new Set()});
    nodes.get(job.groupKey).jobs.push({...job, index});
  }
  for (const node of nodes.values()) {
    for (const job of node.jobs) {
      for (const id of job.needs) {
        const key = keyByID.get(id); // unknown needs are dropped
        if (key !== undefined && key !== node.key) node.needs.add(key);
      }
    }
    const [first] = node.jobs;
    node.name = node.jobs.length === 1 ? first.name : node.key.slice(node.key.lastIndexOf('/') + 1);
    node.status = statusOrder.find((s) => node.jobs.some((j) => j.status === s)) ?? first.status;
  }

  const levels = new Map();
  const levelOf = (node, visiting = new Set()) => {
    if (levels.has(node.key)) return levels.get(node.key);
    visiting.add(node.key);
    let level = 0;
    for (const key of node.needs) {
      if (!visiting.has(key)) level = Math.max(level, levelOf(nodes.get(key), visiting) + 1); // visiting: cycle guard
    }
    visiting.delete(node.key);
    levels.set(node.key, level);
    return level;
  };

  const columns = [];
  const edges = [];
  for (const node of nodes.values()) {
    (columns[levelOf(node)] ??= []).push(node);
    for (const key of node.needs) edges.push([key, node.key]);
  }
  return {columns: columns.filter(Boolean), edges};
}
