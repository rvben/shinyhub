(() => {
  const toggles = {
    __drawer: {
      label: 'label.md-header__button[for="__drawer"]',
      content: '.md-sidebar--primary',
      controls: 'shinyhub-navigation',
      focus: '.md-sidebar--primary a[href]',
      media: '(max-width: 76.234375em)',
    },
    __search: {
      label: 'label.md-header__button[for="__search"]',
      content: '[data-md-component="search"][role="dialog"]',
      controls: 'shinyhub-search',
      focus: '[role="combobox"][aria-label="Search"], .md-search__input, .md-search input:not([type="hidden"])',
    },
    __toc: {
      label: 'label.md-sidebar-button[for="__toc"]',
      content: '.md-sidebar--secondary .md-sidebar__inner',
      controls: 'shinyhub-page-navigation',
      focus: '.md-sidebar--secondary a[href]',
      media: '(max-width: 59.984375em)',
      ariaLabel: 'On this page',
    },
  };

  const inputFor = (id) => document.getElementById(id);
  const buttonFor = (id) => document.querySelector(`[data-shinyhub-toggle="${id}"]`);

  function isOverlay(spec) {
    return !spec.media || window.matchMedia(spec.media).matches;
  }

  function sync(id) {
    const spec = toggles[id];
    const input = inputFor(id);
    const button = buttonFor(id);
    const content = document.querySelector(spec.content);

    if (!input || !button || !content) return;

    const expanded = input.checked;
    button.setAttribute('aria-expanded', String(expanded));
    content.inert = isOverlay(spec) && !expanded;
  }

  function focusWhenReady(selector, attempts = 12) {
    const target = document.querySelector(selector);
    if (target) {
      target.focus();
      return;
    }

    if (attempts > 0) {
      window.setTimeout(() => focusWhenReady(selector, attempts - 1), 25);
    }
  }

  function setExpanded(id, expanded, { focusContent = false, restoreFocus = false } = {}) {
    const spec = toggles[id];
    const input = inputFor(id);
    const button = buttonFor(id);
    if (!input || !button || input.checked === expanded) return;

    input.checked = expanded;
    sync(id);
    input.dispatchEvent(new Event('change', { bubbles: true }));

    if (expanded && focusContent) {
      focusWhenReady(spec.focus);
    } else if (!expanded && restoreFocus) {
      button.focus();
    }
  }

  function enhance(id) {
    const spec = toggles[id];
    const input = inputFor(id);
    const content = document.querySelector(spec.content);
    if (!input || !content) return;

    content.id = spec.controls;

    const label = document.querySelector(spec.label);
    if (label) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = label.className;
      button.innerHTML = label.innerHTML;
      button.dataset.shinyhubToggle = id;
      button.setAttribute('aria-controls', spec.controls);
      button.setAttribute('aria-label', label.getAttribute('aria-label') || spec.ariaLabel || 'Toggle');
      if (label.title) button.title = label.title;
      label.replaceWith(button);
    }

    sync(id);
  }

  function enhancePage() {
    Object.keys(toggles).forEach(enhance);
  }

  document.addEventListener('click', (event) => {
    const button = event.target.closest('[data-shinyhub-toggle]');
    if (!button) return;

    const id = button.dataset.shinyhubToggle;
    const input = inputFor(id);
    if (!input) return;

    setExpanded(id, !input.checked, { focusContent: !input.checked });
  });

  document.addEventListener('change', (event) => {
    if (event.target.id in toggles) sync(event.target.id);
  }, true);

  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;

    const openId = ['__search', '__drawer', '__toc'].find((id) => inputFor(id)?.checked);
    if (!openId) return;

    event.preventDefault();
    setExpanded(openId, false, { restoreFocus: true });
  });

  Object.values(toggles).forEach((spec) => {
    if (spec.media) window.matchMedia(spec.media).addEventListener('change', enhancePage);
  });

  if (typeof document$ !== 'undefined') {
    document$.subscribe(enhancePage);
  } else if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', enhancePage, { once: true });
  } else {
    enhancePage();
  }
})();
