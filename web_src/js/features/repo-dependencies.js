// the filters of the Dependencies page apply when a select changes (the search box submits on Enter, the button stays for no-JS)
export function initRepoDependencies() {
  document.addEventListener('change', (e) => {
    const select = e.target;
    if (!(select instanceof HTMLSelectElement)) return;
    const form = select.closest('form.repo-dependencies-filters');
    if (form) form.requestSubmit();
  });
}
