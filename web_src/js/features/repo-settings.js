import $ from 'jquery';
import {minimatch} from 'minimatch';
import {onInputDebounce, toggleElem} from '../utils/dom.js';
import {POST} from '../modules/fetch.js';
import {createCodemirror} from './codemirror.ts';

const {appSubUrl, i18n} = window.config;

export function initRepoSettingsCollaboration() {
  // Change collaborator access mode
  for (const dropdownEl of document.querySelectorAll('.page-content.repository .dialog-dropdown.access-mode')) {
    const url = dropdownEl.getAttribute('data-url');
    const uid = dropdownEl.getAttribute('data-uid');
    const textEl = dropdownEl.querySelector('.text');
    for (const buttonEl of dropdownEl.querySelectorAll('button[data-mode]')) {
      const mode = buttonEl.getAttribute('data-mode');
      buttonEl.addEventListener('click', async () => {
        dropdownEl.classList.add('is-loading');
        dropdownEl.removeAttribute('open');

        const data = new FormData();
        data.append('uid', uid);
        data.append('mode', mode);

        try {
          await POST(url, {data});
          textEl.textContent = buttonEl.textContent;
        } catch {
          showErrorToast(i18n.network_error);
        }
        dropdownEl.classList.remove('is-loading');
      }, {passive: true});
    }
  }
}

export function initRepoSettingSearchTeamBox() {
  const searchTeamBox = document.getElementById('search-team-box');
  if (!searchTeamBox) return;

  $(searchTeamBox).search({
    minCharacters: 2,
    apiSettings: {
      url: `${appSubUrl}/org/${searchTeamBox.getAttribute('data-org-name')}/teams/-/search?q={query}`,
      onResponse(response) {
        const items = [];
        for (const item of response.data) {
          items.push({
            title: item.name,
            description: `${item.permission} access`, // TODO: translate this string
          });
        }
        return {results: items};
      },
    },
    searchFields: ['name', 'description'],
    showNoResults: false,
  });
}

export function initRepoSettingGitHook() {
  if (!$('.edit.githook').length) return;
  const filename = document.querySelector('.hook-filename').textContent;
  const _promise = createCodemirror($('#content')[0], filename, {language: 'shell'});
}

export function initRepoSettingBranches() {
  if (!document.querySelector('.repository.settings.branches')) return;

  for (const el of document.getElementsByClassName('toggle-target-enabled')) {
    el.addEventListener('change', function () {
      const target = document.querySelector(this.getAttribute('data-target'));
      target?.classList.toggle('disabled', !this.checked);
    });
  }

  for (const el of document.getElementsByClassName('toggle-target-disabled')) {
    el.addEventListener('change', function () {
      const target = document.querySelector(this.getAttribute('data-target'));
      if (this.checked) target?.classList.add('disabled'); // only disable, do not auto enable
    });
  }

  document.getElementById('dismiss_stale_approvals')?.addEventListener('change', function () {
    document.getElementById('ignore_stale_approvals_box')?.classList.toggle('disabled', this.checked);
  });

  // show the `Matched` mark for the status checks that match the pattern
  const markMatchedStatusChecks = () => {
    const patterns = (document.getElementById('status_check_contexts').value || '').split(/[\r\n]+/);
    const validPatterns = patterns.map((item) => item.trim()).filter(Boolean);
    const marks = document.getElementsByClassName('status-check-matched-mark');

    for (const el of marks) {
      let matched = false;
      const statusCheck = el.getAttribute('data-status-check');
      for (const pattern of validPatterns) {
        if (minimatch(statusCheck, pattern)) {
          matched = true;
          break;
        }
      }
      toggleElem(el, matched);
    }
  };
  markMatchedStatusChecks();
  document.getElementById('status_check_contexts').addEventListener('input', onInputDebounce(markMatchedStatusChecks));

  initApprovalRules();
}

const showJsOnly = (root) => {
  for (const el of root.querySelectorAll('[data-js-only]')) toggleElem(el, true);
};

const setTitle = (card) => {
  const title = card.querySelector('.approval-rule-title');
  title.textContent = card.querySelector('[name="approval_rule_name"]').value.trim() || title.getAttribute('data-default');
};

// Cards of approval rules: add, remove and validate in the browser. Without JS the existing cards still save as before.
function initApprovalRules() {
  const rules = document.querySelector('.approval-rules');
  if (!rules) return;
  const list = rules.querySelector('.approval-rule-cards');
  const addButton = rules.querySelector('.approval-rule-add');
  const cards = () => list.querySelectorAll('.approval-rule-card');
  const update = () => {
    const full = cards().length >= Number(rules.getAttribute('data-max'));
    addButton.disabled = full;
    toggleElem(rules.querySelector('.approval-rule-limit'), full);
  };

  showJsOnly(rules);
  update();

  addButton.addEventListener('click', () => {
    const card = rules.querySelector('template').content.firstElementChild.cloneNode(true);
    showJsOnly(card);
    list.append(card);
    $(card).find('.ui.dropdown').dropdown();
    update();
  });
  list.addEventListener('click', (e) => {
    e.target.closest('.approval-rule-remove')?.closest('.approval-rule-card').remove();
    update();
  });
  list.addEventListener('input', (e) => {
    if (e.target.name === 'approval_rule_name') setTitle(e.target.closest('.approval-rule-card'));
  });

  // drop empty cards, refuse half-filled ones
  rules.closest('form').addEventListener('submit', (e) => {
    let firstBad;
    for (const card of cards()) {
      const value = (name) => card.querySelector(`[name="${name}"]`)?.value.trim() ?? '';
      const hasApprovers = value('approval_rule_users') || value('approval_rule_teams') || value('approval_rule_code_owners') === 'true';
      if (!value('approval_rule_name') && !hasApprovers && !value('approval_rule_paths') && value('approval_rule_count') === '1') {
        card.remove();
        continue;
      }
      const bad = !value('approval_rule_name') || !hasApprovers;
      toggleElem(card.querySelector('.approval-rule-error'), bad);
      for (const field of card.querySelectorAll('.field')) field.classList.toggle('error', bad);
      if (bad) firstBad ??= card;
    }
    update();
    if (firstBad) {
      e.preventDefault();
      firstBad.scrollIntoView({block: 'center'});
    }
  });
}
