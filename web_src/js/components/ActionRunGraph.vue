<script>
import {SvgIcon} from '../svg.js';
import ActionRunStatus from './ActionRunStatus.vue';
import {layoutGraph} from './action-run-graph.js';

const collapsedStorageKey = 'actions-run-graph-collapsed';

export default {
  name: 'ActionRunGraph',
  components: {SvgIcon, ActionRunStatus},
  props: {
    jobs: {type: Array, required: true},
    runLink: {type: String, required: true},
    currentJobIndex: {type: Number, default: -1},
    locale: {type: Object, required: true},
  },
  data() {
    let collapsed = false;
    try {
      collapsed = localStorage.getItem(collapsedStorageKey) === 'true';
    } catch { /* storage unavailable: keep the default */ }
    return {collapsed, hovered: null, expanded: {}, lines: []};
  },
  computed: {
    graph() {
      return layoutGraph(this.jobs);
    },
    related() {
      const keys = new Set([this.hovered]);
      for (const [from, to] of this.graph.edges) {
        if (to === this.hovered) keys.add(from);
        if (from === this.hovered) keys.add(to);
      }
      return keys;
    },
  },
  watch: {
    graph() {
      this.$nextTick(this.measure);
    },
  },
  mounted() {
    this.measure();
    window.addEventListener('resize', this.measure);
  },
  unmounted() {
    window.removeEventListener('resize', this.measure);
  },
  methods: {
    toggle() {
      this.collapsed = !this.collapsed;
      try {
        localStorage.setItem(collapsedStorageKey, String(this.collapsed));
      } catch { /* ignore */ }
      this.$nextTick(this.measure);
    },
    toggleGroup(key) {
      this.expanded[key] = !this.expanded[key];
      this.$nextTick(this.measure);
    },
    countLine(node) {
      return this.locale.graphMatrixCount
        .replace('%[1]d', node.jobs.length)
        .replace('%[2]d', node.jobs.filter((j) => j.status === 'success').length)
        .replace('%[3]d', node.jobs.filter((j) => j.status === 'failure').length);
    },
    // Draw the arrows between the boxes as they are laid out by the browser.
    measure() {
      const root = this.$refs.canvas;
      if (!root) return;
      const origin = root.getBoundingClientRect();
      const nodes = Array.from(root.querySelectorAll('[data-key]'));
      const box = (key) => nodes.find((el) => el.dataset.key === key)?.getBoundingClientRect();
      this.lines = this.graph.edges.flatMap(([from, to]) => {
        const a = box(from), b = box(to);
        if (!a || !b) return [];
        const x1 = a.right - origin.left, y1 = a.top + a.height / 2 - origin.top;
        const x2 = b.left - origin.left, y2 = b.top + b.height / 2 - origin.top;
        const mid = (x1 + x2) / 2;
        return [{key: `${from}>${to}`, from, to, d: `M${x1},${y1} C${mid},${y1} ${mid},${y2} ${x2},${y2}`}];
      });
    },
  },
};
</script>
<template>
  <div class="action-run-graph">
    <button class="action-run-graph-toggle" type="button" @click="toggle" :aria-expanded="!collapsed">
      <SvgIcon :name="collapsed ? 'octicon-chevron-right' : 'octicon-chevron-down'" :size="16"/>
      {{ locale.graphTitle }}
    </button>
    <div v-show="!collapsed" ref="canvas" class="action-run-graph-canvas">
      <svg class="action-run-graph-lines">
        <path v-for="line in lines" :key="line.key" :d="line.d" :class="{active: hovered && related.has(line.from) && related.has(line.to)}"/>
      </svg>
      <div v-for="(column, level) in graph.columns" :key="level" class="action-run-graph-column">
        <div
          v-for="node in column" :key="node.key" :data-key="node.key" class="action-run-graph-node"
          :class="{dim: hovered && !related.has(node.key)}"
          @mouseenter="hovered = node.key" @mouseleave="hovered = null"
        >
          <a
            v-if="node.jobs.length === 1" class="action-run-graph-job" :href="`${runLink}/jobs/${node.jobs[0].index}`"
            :class="{selected: node.jobs[0].index === currentJobIndex}"
          >
            <ActionRunStatus :locale-status="locale.status[node.status]" :status="node.status"/>
            <span class="gt-ellipsis">{{ node.name }}</span>
            <span class="action-run-graph-duration">{{ node.jobs[0].duration }}</span>
          </a>
          <template v-else>
            <button class="action-run-graph-job" type="button" @click="toggleGroup(node.key)" :aria-expanded="!!expanded[node.key]">
              <ActionRunStatus :locale-status="locale.status[node.status]" :status="node.status"/>
              <span class="gt-ellipsis">{{ node.name }}</span>
              <SvgIcon :name="expanded[node.key] ? 'octicon-chevron-down' : 'octicon-chevron-right'" :size="14"/>
            </button>
            <div class="action-run-graph-count">
              {{ countLine(node) }}
            </div>
            <div v-if="expanded[node.key]" class="action-run-graph-children">
              <a v-for="job in node.jobs" :key="job.id" :href="`${runLink}/jobs/${job.index}`" :class="{selected: job.index === currentJobIndex}">
                <ActionRunStatus :locale-status="locale.status[job.status]" :status="job.status" :size="14"/>
                <span class="gt-ellipsis">{{ job.name }}</span>
                <span class="action-run-graph-duration">{{ job.duration }}</span>
              </a>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>
<style scoped>
.action-run-graph {
  margin-bottom: 12px;
  border: 1px solid var(--color-secondary);
  border-radius: var(--border-radius);
  background: var(--color-box-body);
}

.action-run-graph-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: none;
  color: var(--color-text);
  font-weight: var(--font-weight-semibold);
  cursor: pointer;
}

.action-run-graph-canvas {
  position: relative;
  display: flex;
  gap: 48px;
  padding: 12px;
  overflow-inline: auto;
}

.action-run-graph-lines {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
  fill: none;
}

.action-run-graph-lines path {
  stroke: var(--color-secondary-dark-4);
  stroke-width: 1.5;
}

.action-run-graph-lines path.active {
  stroke: var(--color-primary);
}

.action-run-graph-column {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 8px;
  min-width: 180px;
}

.action-run-graph-node {
  position: relative; /* above the arrows */
  border: 1px solid var(--color-secondary);
  border-radius: var(--border-radius);
  background: var(--color-box-header);
}

.action-run-graph-node.dim {
  opacity: 0.5;
}

.action-run-graph-job {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 8px;
  border: 0;
  background: none;
  color: var(--color-text);
  cursor: pointer;
}

.action-run-graph-job.selected,
.action-run-graph-children a.selected {
  font-weight: var(--font-weight-semibold);
}

.action-run-graph-duration {
  margin-inline-start: auto;
  color: var(--color-text-light-2);
  font-size: 12px;
}

.action-run-graph-count {
  padding: 0 8px 8px;
  color: var(--color-text-light-2);
  font-size: 12px;
}

.action-run-graph-children {
  display: flex;
  flex-direction: column;
  border-top: 1px solid var(--color-secondary);
}

.action-run-graph-children a {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  color: var(--color-text);
}
</style>
