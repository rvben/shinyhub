const TOKEN_KEY = 'shinyhost_token';

document.addEventListener('alpine:init', () => {
  Alpine.data('auth', () => ({
    get username() {
      const t = localStorage.getItem(TOKEN_KEY);
      if (!t) return null;
      try { return JSON.parse(atob(t.split('.')[1])).sub; } catch { return null; }
    },
    logout() {
      localStorage.removeItem(TOKEN_KEY);
      window.location.reload();
    }
  }));

  Alpine.data('appList', () => ({
    loggedIn: !!localStorage.getItem(TOKEN_KEY),
    apps: [],
    username: '',
    password: '',
    error: '',

    async login() {
      this.error = '';
      const r = await fetch('/api/auth/login', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({username: this.username, password: this.password})
      });
      if (!r.ok) { this.error = 'Invalid credentials'; return; }
      const {token} = await r.json();
      localStorage.setItem(TOKEN_KEY, token);
      this.loggedIn = true;
      this.refresh();
    },

    async refresh() {
      const token = localStorage.getItem(TOKEN_KEY);
      if (!token) return;
      const r = await fetch('/api/apps', {
        headers: {'Authorization': 'Bearer ' + token}
      });
      if (r.status === 401) { this.loggedIn = false; return; }
      this.apps = (await r.json()) || [];
    },

    async restart(slug) {
      const token = localStorage.getItem(TOKEN_KEY);
      await fetch('/api/apps/' + slug + '/restart', {
        method: 'POST',
        headers: {'Authorization': 'Bearer ' + token}
      });
      setTimeout(() => this.refresh(), 1000);
    },

    async rollback(slug) {
      const token = localStorage.getItem(TOKEN_KEY);
      await fetch('/api/apps/' + slug + '/rollback', {
        method: 'PUT',
        headers: {'Authorization': 'Bearer ' + token}
      });
      setTimeout(() => this.refresh(), 1000);
    },

    init() { if (this.loggedIn) this.refresh(); }
  }));
});
