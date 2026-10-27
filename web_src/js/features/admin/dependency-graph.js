import {toggleElem} from '../../utils/dom.js';
import {POST} from '../../modules/fetch.js';

// The dependency graph settings: the mirror URL, sign-in and token show only with the mirror source, the username only
// with Basic sign-in and the token only with a sign-in; with the source None, the download settings and the test hide
// too. Without JS every field shows, and the server ignores the ones the choices don't use.
export function initAdminDependencyGraph() {
  const form = document.querySelector('.dependency-graph-settings');
  if (!form) return;
  const tokenLabel = form.querySelector('label[for="dg-token"]');
  const test = form.querySelector('#dependency-graph-test');
  const testHint = form.querySelector('#dg-test-hint');
  const testResult = form.querySelector('#dg-test-result');
  const update = () => {
    const source = form.querySelector('input[name="source"]:checked')?.value;
    const signIn = form.querySelector('input[name="sign_in"]:checked')?.value;
    toggleElem(form.querySelector('#dg-mirror-fields'), source === 'mirror');
    toggleElem(form.querySelector('#dg-user-field'), signIn === 'basic');
    toggleElem(form.querySelector('#dg-token-field'), signIn !== 'none');
    const removeToken = form.querySelector('#dg-remove-token-field');
    if (removeToken) toggleElem(removeToken, signIn !== 'none');
    const unused = form.querySelector('#dg-token-unused');
    if (unused) toggleElem(unused, source !== 'mirror');
    toggleElem(form.querySelector('#dg-download-fields'), source !== 'none');
    if (test) toggleElem(test, source !== 'none');
    if (tokenLabel) tokenLabel.textContent = signIn === 'basic' ? tokenLabel.getAttribute('data-label-basic') : tokenLabel.getAttribute('data-label-token');
  };

  // the test uses the saved settings: with unsaved changes it would test something else
  const saved = new URLSearchParams(new FormData(form)).toString();
  const updateTest = () => {
    if (!test) return;
    // a refused save shows the posted values, which aren't saved either
    const dirty = form.hasAttribute('data-unsaved') || new URLSearchParams(new FormData(form)).toString() !== saved;
    test.disabled = dirty;
    toggleElem(testHint, dirty);
  };

  form.addEventListener('change', (e) => {
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
  updateTest();
}
