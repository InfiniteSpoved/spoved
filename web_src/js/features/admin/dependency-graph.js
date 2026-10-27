import {toggleElem} from '../../utils/dom.js';
import {POST} from '../../modules/fetch.js';

// The dependency graph settings: the mirror URL, sign-in and token show only with the mirror source, the username only
// with Basic sign-in and the token only with a sign-in; with the source None, the download settings and the test hide
// too. A part holding a refused field always shows. Without JS every field shows, and the server ignores the ones the
// choices don't use. The auditor sees the source and sign-in as text (data-source, data-sign-in).
// a part holding a refused field always shows
function show(el, on) {
  if (el) toggleElem(el, on || Boolean(el.querySelector('[aria-invalid="true"]')));
}

// a hidden "Remove the token" box is disabled, so only the one shown is posted
function showRemoveToken(el, on) {
  if (!el) return;
  toggleElem(el, on);
  el.querySelector('input').disabled = !on;
}

export function initAdminDependencyGraph() {
  const form = document.querySelector('.dependency-graph-settings');
  if (!form) return;
  const tokenLabel = form.querySelector('label[for="dg-token"]');
  const test = form.querySelector('#dependency-graph-test');
  const testHint = form.querySelector('#dg-test-hint');
  const testResult = form.querySelector('#dg-test-result');
  const choice = (name, attr) => form.querySelector(`input[name="${name}"]:checked`)?.value ?? form.querySelector(`[${attr}]`)?.getAttribute(attr);

  // the test uses the saved settings: with unsaved changes it would test something else
  const saved = new URLSearchParams(new FormData(form)).toString();
  const updateTest = () => {
    if (!test) return;
    // a refused save shows the posted values, which aren't saved either
    const dirty = form.hasAttribute('data-unsaved') || new URLSearchParams(new FormData(form)).toString() !== saved;
    test.disabled = dirty;
    toggleElem(testHint, dirty && !test.classList.contains('tw-hidden'));
    if (dirty) {
      testResult.className = '';
      testResult.textContent = '';
    }
  };

  const update = () => {
    const source = choice('source', 'data-source');
    const signIn = choice('sign_in', 'data-sign-in');
    show(form.querySelector('#dg-mirror-fields'), source === 'mirror');
    show(form.querySelector('#dg-user-field'), signIn === 'basic');
    show(form.querySelector('#dg-token-field'), signIn !== 'none');
    showRemoveToken(form.querySelector('#dg-remove-token-field'), source === 'mirror' && signIn !== 'none');
    showRemoveToken(form.querySelector('#dg-token-unused'), source !== 'mirror');
    show(form.querySelector('#dg-download-fields'), source !== 'none');
    if (test) toggleElem(test, source !== 'none');
    if (tokenLabel) tokenLabel.textContent = signIn === 'basic' ? tokenLabel.getAttribute('data-label-basic') : tokenLabel.getAttribute('data-label-token');
    updateTest();
  };

  form.addEventListener('change', (e) => {
    if (e.target.matches('input[name="remove_token"]')) {
      for (const box of form.querySelectorAll('input[name="remove_token"]')) box.checked = e.target.checked;
    }
    if (e.target.matches('input[name="source"], input[name="sign_in"]')) update();
    updateTest();
  });
  form.addEventListener('input', updateTest);
  test?.addEventListener('click', async (e) => {
    e.preventDefault();
    test.classList.add('loading');
    testResult.textContent = '';
    try {
      const data = new FormData();
      data.append('inline', '1');
      const resp = await POST(test.getAttribute('formaction'), {data});
      const json = await resp.json();
      testResult.className = json.ok ? 'text green' : 'dg-error';
      testResult.textContent = json.message;
    } catch {
      form.requestSubmit(test); // without the inline answer, the flash of a normal submit
    } finally {
      test.classList.remove('loading');
    }
  });
  update();
}
