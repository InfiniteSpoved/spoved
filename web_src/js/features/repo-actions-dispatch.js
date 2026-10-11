import {isoToZonedLocal, zonedLocalToISO, zoneLabel} from '../utils/time.js';

const day = 24 * 3600 * 1000;

function zoneOf(form) {
  return form.getAttribute('data-time-zone') || Intl.DateTimeFormat().resolvedOptions().timeZone;
}

// the forms are swapped by htmx, so everything is looked up when it is needed
function preparePicker(form) {
  const picker = form.querySelector('input[data-run-at]');
  const zone = zoneOf(form);
  const now = Date.now();
  picker.min = isoToZonedLocal(now, zone);
  picker.max = isoToZonedLocal(now + Number(form.getAttribute('data-max-days')) * day, zone);
  const label = form.querySelector('.time-zone-label');
  if (label) label.textContent = zoneLabel(zone);
  const current = form.getAttribute('data-run-at-iso');
  if (current && !picker.value) picker.value = isoToZonedLocal(current, zone);
}

export function initRepoActionsDispatch() {
  document.addEventListener('change', (e) => {
    const radio = e.target.closest?.('input[name="run_mode"]');
    if (!radio) return;
    const form = radio.form;
    const later = form.querySelector('input[name="run_mode"]:checked').value === 'later';
    form.querySelector('[data-run-later]').classList.toggle('tw-hidden', !later);
    form.querySelector('input[data-run-at]').required = later;
    if (later) preparePicker(form);
  });
  // <details> of "Change time": toggle does not bubble
  document.addEventListener('toggle', (e) => {
    const form = e.target.closest?.('details.change-time')?.querySelector('form');
    if (form && e.target.open) preparePicker(form);
  }, true);
  document.addEventListener('submit', (e) => {
    const form = e.target;
    const picker = form.querySelector?.('input[data-run-at]');
    const hidden = form.querySelector?.('input[name="run_at"]');
    if (!picker || !hidden) return;
    const radio = form.querySelector('input[name="run_mode"]:checked');
    hidden.value = (!radio || radio.value === 'later') && picker.value ? zonedLocalToISO(picker.value, zoneOf(form)) : '';
  }, true);
}
