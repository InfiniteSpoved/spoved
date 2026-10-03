import $ from 'jquery';
import {htmlEscape} from 'escape-goat';

const {appSubUrl} = window.config;

export function initOrgTeamSearchUserGroupBox() {
  const searchBox = document.getElementById('search-user-group-box');
  if (!searchBox) return;

  $(searchBox).search({
    minCharacters: 1,
    apiSettings: {
      url: `${searchBox.getAttribute('data-url')}?q={query}`,
      onResponse(response) {
        const query = searchBox.querySelector('input').value.toUpperCase();
        const items = [];
        for (const item of response.data) {
          const result = {title: item.name, description: htmlEscape(item.description)};
          if (item.name.toUpperCase() === query) {
            items.unshift(result); // put the exact match first
          } else {
            items.push(result);
          }
        }
        return {results: items};
      },
    },
    searchFields: ['name', 'description'],
    showNoResults: false,
  });
}

export function initOrgTeamSearchRepoBox() {
  const $searchRepoBox = $('#search-repo-box');
  $searchRepoBox.search({
    minCharacters: 2,
    apiSettings: {
      url: `${appSubUrl}/repo/search?q={query}&uid=${$searchRepoBox.data('uid')}`,
      onResponse(response) {
        const items = [];
        for (const item of response.data) {
          items.push({
            title: item.repository.full_name.split('/')[1],
            description: item.repository.full_name,
          });
        }
        return {results: items};
      },
    },
    searchFields: ['full_name'],
    showNoResults: false,
  });
}
