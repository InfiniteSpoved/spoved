import {toggleElem} from '../../utils/dom.js';

// The dependency graph settings: the mirror URL, sign-in and token show only with the mirror source, the username only
// with Basic sign-in and the token only with a sign-in. Without JS every field shows, and the server ignores the ones
// the choices don't use.
export function initAdminDependencyGraph() {
  const form = document.querySelector('.dependency-graph-settings');
  if (!form) return;
  const update = () => {
    const source = form.querySelector('input[name="source"]:checked')?.value;
    const signIn = form.querySelector('input[name="sign_in"]:checked')?.value;
    toggleElem(form.querySelector('#dg-mirror-fields'), source === 'mirror');
    toggleElem(form.querySelector('#dg-user-field'), signIn === 'basic');
    toggleElem(form.querySelector('#dg-token-field'), signIn !== 'none');
  };
  form.addEventListener('change', (e) => {
    if (e.target.matches('input[name="source"], input[name="sign_in"]')) update();
  });
  update();
}
