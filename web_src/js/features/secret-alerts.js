import {showModal} from '../modules/modal.ts';
import {POST} from '../modules/fetch.js';

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

// the list: a bar with the number of selected alerts
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
  // the switches (repository, organization, Site administration): an input that carries data-confirm-modal asks first when the
  // change would turn scanning off ("off") or a rule on ("on"); the modal's button submits the form again, marked
  document.addEventListener('submit', (e) => {
    const form = e.target.closest?.('.secret-confirm-form');
    if (!form || form.hasAttribute('data-confirmed')) return;
    for (const input of form.querySelectorAll('input[data-confirm-modal]')) {
      const turnsOn = input.getAttribute('data-confirm-when') === 'on';
      // data-confirm-requires: only while that other input is checked (the lock asks only when scanning stays on)
      const requires = input.getAttribute('data-confirm-requires');
      if (requires && !form.querySelector(requires)?.checked) continue;
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
  // custom patterns: a delete asks for the name (the server checks it again)
  document.addEventListener('input', (e) => {
    const input = e.target.closest?.('input[data-confirm-name]');
    if (!input) return;
    const button = document.querySelector(input.getAttribute('data-confirm-button'));
    if (button) button.disabled = input.value.trim().toLowerCase() !== input.getAttribute('data-confirm-name').toLowerCase();
  });
  // custom patterns: Test posts the form's fields and shows the answer; the server stores nothing
  document.addEventListener('click', async (e) => {
    const button = e.target.closest?.('#secret-pattern-test');
    if (!button) return;
    const form = button.closest('form');
    const out = form.querySelector('#secret-pattern-result');
    const show = (ok, title, text) => {
      const box = document.createElement('div');
      box.className = `ui message ${ok ? 'positive' : 'negative'}`;
      box.setAttribute('role', ok ? 'status' : 'alert');
      const head = document.createElement('div');
      head.className = 'header';
      head.textContent = title;
      box.append(head, document.createTextNode(text));
      out.replaceChildren(box);
    };
    button.disabled = true;
    try {
      const resp = await POST(button.getAttribute('data-url'), {data: new FormData(form)});
      if (resp.ok) {
        const body = await resp.json();
        show(body.ok, body.title, body.message);
      } else {
        show(false, button.getAttribute('data-failed'), resp.status === 429 ? await resp.text() : '');
      }
    } catch {
      show(false, button.getAttribute('data-failed'), '');
    } finally {
      button.disabled = false;
    }
  });
  // the phone's menu bar scrolls: the current page must be in view
  const menu = document.querySelector('.repo-security-menu');
  const active = menu?.querySelector('.item.active');
  if (active) menu.scrollLeft = Math.max(0, active.offsetLeft - (menu.clientWidth - active.offsetWidth) / 2); // the bar only, not the page
}
