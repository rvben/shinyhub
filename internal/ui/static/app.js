const TOKEN_KEY = 'shinyhub_token';

document.addEventListener('alpine:init', () => {
  Alpine.store('auth', {
    username: null,
    init() {
      // Handle OAuth redirect: server sends /?token=<jwt> after GitHub login.
      const params = new URLSearchParams(window.location.search);
      const redirectToken = params.get('token');
      if (redirectToken) {
        localStorage.setItem(TOKEN_KEY, redirectToken);
        document.cookie = `shiny_session=${redirectToken}; path=/; SameSite=Lax`;
        window.history.replaceState({}, '', '/');
      }
      const t = localStorage.getItem(TOKEN_KEY);
      if (!t) return;
      try { this.username = JSON.parse(atob(t.split('.')[1])).sub; } catch {}
    },
    logout() {
      localStorage.removeItem(TOKEN_KEY);
      document.cookie = 'shiny_session=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT';
      window.location.reload();
    }
  });

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
      document.cookie = `shiny_session=${token}; path=/; SameSite=Lax`;
      try { Alpine.store('auth').username = JSON.parse(atob(token.split('.')[1])).sub; } catch {}
      this.loggedIn = true;
      this.refresh();
    },

    async refresh() {
      const token = localStorage.getItem(TOKEN_KEY);
      if (!token) return;
      try {
        const r = await fetch('/api/apps', {
          headers: {'Authorization': 'Bearer ' + token}
        });
        if (r.status === 401) { this.loggedIn = false; return; }
        if (!r.ok) { this.error = 'Failed to load apps'; return; }
        this.apps = (await r.json()) || [];
      } catch {
        this.error = 'Network error';
      }
    },

    async restart(slug) {
      const token = localStorage.getItem(TOKEN_KEY);
      try {
        const r = await fetch('/api/apps/' + slug + '/restart', {
          method: 'POST',
          headers: {'Authorization': 'Bearer ' + token}
        });
        if (!r.ok) { this.error = 'Restart failed'; return; }
        setTimeout(() => this.refresh(), 1000);
      } catch {
        this.error = 'Network error';
      }
    },

    async rollback(slug) {
      const token = localStorage.getItem(TOKEN_KEY);
      try {
        const r = await fetch('/api/apps/' + slug + '/rollback', {
          method: 'PUT',
          headers: {'Authorization': 'Bearer ' + token}
        });
        if (!r.ok) { this.error = 'Rollback failed'; return; }
        setTimeout(() => this.refresh(), 1000);
      } catch {
        this.error = 'Network error';
      }
    },

    init() { if (this.loggedIn) this.refresh(); }
  }));
});
