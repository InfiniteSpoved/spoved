export function initRepoPullRequestCommitStatus() {
  for (const btn of document.querySelectorAll('.commit-status-hide-checks')) {
    const panel = btn.closest('.commit-status-panel');
    const list = panel.querySelector('.commit-status-list');
    btn.addEventListener('click', () => {
      list.style.maxHeight = list.style.maxHeight ? '' : '0px'; // toggle
      btn.textContent = btn.getAttribute(list.style.maxHeight ? 'data-show-all' : 'data-hide-all');
    });
  }
}

// Open and close the dialogs of the buttons with command/commandfor (Invoker Commands) in browsers without support.
export function initRepoPullStackDialog() {
  if ('commandForElement' in HTMLButtonElement.prototype) return;
  document.addEventListener('click', (e) => {
    const btn = e.target.closest?.('button[command][commandfor]');
    const dialog = btn && document.getElementById(btn.getAttribute('commandfor'));
    if (!(dialog instanceof HTMLDialogElement)) return;
    if (btn.getAttribute('command') === 'show-modal') dialog.showModal();
    else if (btn.getAttribute('command') === 'close') dialog.close();
  });
}
