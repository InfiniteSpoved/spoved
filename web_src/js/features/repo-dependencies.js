// the filters of the Dependencies page apply when a select changes (the search box submits on Enter, the button stays for no-JS)
export function initRepoDependencies() {
  document.addEventListener('change', (e) => {
    const select = e.target;
    if (!(select instanceof HTMLSelectElement)) return;
    const form = select.closest('form.repo-dependencies-filters');
    if (form) form.requestSubmit();
  });
}

// the security alert filters are always open on a wide screen and fold behind "Filters" on a phone
export function initRepoSecurityFilters() {
  const box = document.querySelector('details.repo-security-filterbox');
  if (!box) return;
  const wide = window.matchMedia('(min-width: 768px)');
  const sync = () => { if (wide.matches) box.open = true; };
  sync();
  wide.addEventListener('change', sync);
}
