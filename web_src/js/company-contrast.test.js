import {readFileSync} from 'node:fs';
import {dirname, resolve} from 'node:path';
import tinycolor from 'tinycolor2';

// Collects the :root custom properties of a theme, following its unconditional local @imports in order.
function readVars(path, vars = {}) {
  const css = readFileSync(path, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
  for (const [, file] of css.matchAll(/@import\s+"([^"]+)"\s*;/g)) readVars(resolve(dirname(path), file), vars);
  for (const [, block] of css.matchAll(/:root\s*\{([^}]*)\}/g)) {
    for (const [, name, value] of block.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) vars[name] = value.trim();
  }
  return vars;
}

function color(vars, name) {
  let value = vars[name];
  while (value?.startsWith('var(')) value = vars[value.match(/^var\((--[\w-]+)/)[1]];
  const c = tinycolor(value);
  if (!c.isValid() || c.getAlpha() !== 1) throw new Error(`${name} resolves to "${value}", not an opaque colour`);
  return c;
}

const text = [
  ['--text', '--surface'], ['--text', '--surface-raised'], ['--text', '--surface-sunken'], ['--text', '--code-bg'],
  ['--text-muted', '--surface'], ['--text-muted', '--surface-raised'], ['--text-muted', '--surface-sunken'],
  ['--primary', '--surface'], ['--text-on-primary', '--primary'], ['--text-on-primary', '--primary-hover'],
  ['--danger', '--surface'], ['--warning', '--surface'], ['--success', '--surface'],
  ['--danger-text', '--danger-bg'], ['--warning-text', '--warning-bg'], ['--success-text', '--success-bg'],
  ['--text', '--diff-add-bg'], ['--text', '--diff-add-word'], ['--text', '--diff-del-bg'], ['--text', '--diff-del-word'],
  ['--label-text', '--label-bg'],
];
const ui = [['--focus-ring', '--surface'], ['--border-strong', '--surface'], ['--border-strong', '--surface-raised']];

for (const [theme, textMin] of [['light', 4.5], ['dark', 4.5], ['high-contrast', 7]]) {
  test(`theme-company-${theme} contrast`, () => {
    const vars = readVars(resolve(import.meta.dirname, `../css/themes/theme-company-${theme}.css`));
    const failures = [];
    for (const [pairs, min] of [[text, textMin], [ui, 3]]) {
      for (const [fg, bg] of pairs) {
        const ratio = tinycolor.readability(color(vars, fg), color(vars, bg));
        if (ratio < min) failures.push(`${fg} on ${bg}: ${ratio.toFixed(2)} < ${min}`);
      }
    }
    expect(failures).toEqual([]);
  });
}
