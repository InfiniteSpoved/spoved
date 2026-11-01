import {showModal} from '../modules/modal.ts';

function updateBulk() {
  const bar = document.querySelector('#secret-bulk');
  if (!bar) return;
  const boxes = Array.from(document.querySelectorAll('input[form="secret-bulk-form"][name="index"]'));
  const n = boxes.filter((box) => box.checked).length;
  bar.classList.toggle('tw-hidden', n === 0);
  // not data-one: html/template escapes an attribute named data-on... as JavaScript
  const which = n === 1 ? 'one' : 'other';
  const text = bar.getAttribute(`data-text-${which}`) ?? '';
  bar.querySelector('#secret-bulk-count').textContent = text.replace('{n}', String(n));
  const modal = document.querySelector('#secret-bulk-modal');
  if (modal) {
    modal.querySelector('.secret-modal-title').textContent = (bar.getAttribute(`data-title-${which}`) ?? '').replace('{n}', String(n));
    modal.querySelector('.secret-modal-submit').textContent = bar.getAttribute(`data-submit-${which}`) ?? '';
    modal.querySelector('.secret-modal-ask').textContent = bar.getAttribute(`data-ask-${which}`) ?? '';
  }
  const all = document.querySelector('#secret-select-all');
  if (all) all.checked = n > 0 && n === boxes.length;
}

// the list: a bar with the number of selected alerts; the settings: turning scanning off asks first
export function initRepoSecretAlerts() {
  // Won't fix needs a comment, and the comment is limited in bytes (as in the service), not in characters
  document.addEventListener('change', (e) => {
    const reason = e.target.closest?.('.repo-security-reasons input[name="reason"]');
    if (reason) reason.closest('form').querySelector('textarea[name="comment"]').required = reason.value === 'wont_fix';
  });
  document.addEventListener('input', (e) => {
    const area = e.target.closest?.('textarea[data-max-bytes]');
    if (!area) return;
    const tooLong = new TextEncoder().encode(area.value).length > Number(area.getAttribute('data-max-bytes'));
    area.setCustomValidity(tooLong ? area.getAttribute('data-too-long') : '');
  });
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
  // the organization and Site administration switches: an input that carries data-confirm-modal asks first when the
  // change would turn scanning off ("off") or a rule on ("on"); the modal's button submits the form again, marked
  document.addEventListener('submit', (e) => {
    const form = e.target.closest?.('.secret-confirm-form');
    if (!form || form.hasAttribute('data-confirmed')) return;
    for (const input of form.querySelectorAll('input[data-confirm-modal]')) {
      const turnsOn = input.getAttribute('data-confirm-when') === 'on';
      if (input.checked !== input.defaultChecked && input.checked === turnsOn) {
        e.preventDefault();
        showModal(document.querySelector(input.getAttribute('data-confirm-modal')), undefined);
        return;
      }
    }
  });
  document.addEventListener('click', (e) => {
    const ok = e.target.closest?.('.secret-confirm-ok');
    if (ok) document.getElementById(ok.getAttribute('form'))?.setAttribute('data-confirmed', 'true');
  });
  // the phone's menu bar scrolls: the current page must be in view
  const menu = document.querySelector('.repo-security-menu');
  const active = menu?.querySelector('.item.active');
  if (active) menu.scrollLeft = Math.max(0, active.offsetLeft - (menu.clientWidth - active.offsetWidth) / 2); // the bar only, not the page
}
