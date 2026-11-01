import {showModal} from '../modules/modal.ts';

function updateBulk() {
  const bar = document.querySelector('#secret-bulk');
  if (!bar) return;
  const boxes = Array.from(document.querySelectorAll('input[form="secret-bulk-form"][name="index"]'));
  const n = boxes.filter((box) => box.checked).length;
  bar.classList.toggle('tw-hidden', n === 0);
  const text = bar.getAttribute(n === 1 ? 'data-one' : 'data-other') ?? '';
  bar.querySelector('#secret-bulk-count').textContent = text.replace('{n}', String(n));
  const all = document.querySelector('#secret-select-all');
  if (all) all.checked = n > 0 && n === boxes.length;
}

// the list: a bar with the number of selected alerts; the settings: turning scanning off asks first
export function initRepoSecretAlerts() {
  document.addEventListener('change', (e) => {
    const box = e.target.closest?.('#secret-select-all, input[form="secret-bulk-form"][name="index"]');
    if (!box) return;
    if (box.id === 'secret-select-all') {
      for (const one of document.querySelectorAll('input[form="secret-bulk-form"][name="index"]')) one.checked = box.checked;
    }
    updateBulk();
  });
  document.addEventListener('submit', (e) => {
    const form = e.target.closest?.('#secret-scanning-switch');
    if (!form || form.getAttribute('data-was-on') !== 'true' || form.querySelector('input[name="enabled"]').checked) return;
    e.preventDefault();
    showModal(document.querySelector(form.getAttribute('data-modal')), undefined);
  });
}
