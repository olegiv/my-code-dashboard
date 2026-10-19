// SPDX-License-Identifier: GPL-3.0-only
document.addEventListener('alpine:init', () => {
  Alpine.data('connectionForm', () => ({
    provider: 'github',
    baseURL: 'https://github.com',
    get isGitHub() { return this.provider === 'github'; },
    get isGitLab() { return this.provider === 'gitlab'; },
    get gitlabTokenURL() {
      try {
        const instance = new URL(this.baseURL.trim());
        if (instance.protocol !== 'https:' || !instance.hostname || instance.username || instance.password || instance.search || instance.hash) return '';
        instance.pathname = instance.pathname.replace(/\/+$/, '') + '/-/user_settings/personal_access_tokens';
        instance.search = new URLSearchParams({name: 'My Code Dashboard', scopes: 'read_api'}).toString();
        return instance.href;
      } catch {
        return '';
      }
    },
    get gitlabURLInvalid() { return !this.gitlabTokenURL; },
    clearToken() {
      this.$root.querySelector('[name="token"]').value = '';
    },
    existingChanged(event) {
      const option = event.target.selectedOptions[0];
      this.provider = option.dataset.provider || 'github';
      this.baseURL = option.dataset.baseUrl || 'https://github.com';
      this.$root.querySelector('[name="name"]').value = option.dataset.name || '';
      this.clearToken();
    },
    providerChanged() {
      this.baseURL = this.provider === 'github' ? 'https://github.com' : 'https://gitlab.example.com';
      this.clearToken();
    },
  }));
  Alpine.data('tokenInput', () => ({
    visible: false,
    get inputType() { return this.visible ? 'text' : 'password'; },
    get label() { return this.visible ? 'Hide' : 'Show'; },
    toggle() { this.visible = !this.visible; },
  }));
  Alpine.data('repositoryPicker', () => ({
    query: '',
    connectionID: '',
    archive: 'active',
    visibleCount: 0,
    get noMatches() { return this.visibleCount === 0; },
    init() {
      this.connectionID = this.$root.dataset.connectionFilter || '';
      this.filterRows();
    },
    filterRows() {
      const query = this.query.trim().toLocaleLowerCase();
      this.visibleCount = 0;
      for (const row of this.$root.querySelectorAll('tbody tr')) {
        const matchesConnection = !this.connectionID || row.dataset.connection === this.connectionID;
        const archived = row.dataset.archived === 'true';
        const matchesArchive = this.archive === 'all' || (this.archive === 'archived' ? archived : !archived);
        row.hidden = !matchesConnection || !matchesArchive || !row.textContent.toLocaleLowerCase().includes(query);
        if (!row.hidden) this.visibleCount++;
      }
      this.updateSelectAll();
    },
    updateSelectAll() {
      const visible = [...this.$root.querySelectorAll('.repository-checkbox')].filter(input => !input.closest('tr').hidden);
      const checked = visible.filter(input => input.checked).length;
      const toggle = this.$root.querySelector('[data-select-all]');
      toggle.checked = visible.length > 0 && checked === visible.length;
      toggle.indeterminate = checked > 0 && checked < visible.length;
    },
    toggleAll(event) {
      for (const input of this.$root.querySelectorAll('.repository-checkbox')) {
        if (!input.closest('tr').hidden) input.checked = event.target.checked;
      }
      this.updateSelectAll();
    },
  }));
});

document.addEventListener('submit', event => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || form.method.toLowerCase() !== 'post') return;
  // HTMX handles its own requests. Native submissions still get clear feedback.
  if (form.hasAttribute('hx-post')) return;
  for (const button of form.querySelectorAll('button[type="submit"]')) {
    button.setAttribute('aria-busy', 'true');
    button.classList.add('is-busy');
  }
});
