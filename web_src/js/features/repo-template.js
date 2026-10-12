import $ from 'jquery';
import {htmlEscape} from 'escape-goat';
import {hideElem, showElem, toggleElem} from '../utils/dom.js';

const {appSubUrl} = window.config;

export function initRepoTemplateSearch() {
  const $repoTemplate = $('#repo_template');
  const checkTemplate = function () {
    const $templateUnits = $('#template_units');
    const $nonTemplate = $('#non_template');
    if ($repoTemplate.val() !== '' && $repoTemplate.val() !== '0') {
      showElem($templateUnits);
      hideElem($nonTemplate);
    } else {
      hideElem($templateUnits);
      showElem($nonTemplate);
    }
  };
  $repoTemplate.on('change', checkTemplate);
  checkTemplate();

  const changeOwner = function () {
    $('#repo_template_search')
      .dropdown({
        apiSettings: {
          url: `${appSubUrl}/repo/search?q={query}&template=true&priority_owner_id=${$('#uid').val()}`,
          onResponse(response) {
            const filteredResponse = {success: true, results: []};
            filteredResponse.results.push({
              name: '',
              value: '',
            });
            // Parse the response from the api to work with our dropdown
            for (const repo of response.data) {
              filteredResponse.results.push({
                name: htmlEscape(repo.repository.full_name),
                value: repo.repository.id,
              });
            }
            return filteredResponse;
          },
          cache: false,
        },

        fullTextSearch: true,
      });
  };
  $('#uid').on('change', changeOwner);
  changeOwner();

  // the project select offers only the projects of the selected owner
  const repoGroup = document.querySelector('#repo_group');
  if (!repoGroup) return;
  const changeOwnerGroups = function () {
    let any = false;
    for (const option of repoGroup.querySelectorAll('option[data-owner]')) {
      const mine = option.getAttribute('data-owner') === $('#uid').val();
      option.hidden = option.disabled = !mine;
      if (!mine && option.selected) repoGroup.value = '0';
      any ||= mine;
    }
    toggleElem(document.querySelector('#repo_group_label'), any);
  };
  $('#uid').on('change', changeOwnerGroups);
  changeOwnerGroups();
}
