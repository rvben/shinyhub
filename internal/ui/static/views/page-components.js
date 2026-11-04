// Shared page composition. Slots are native nodes: moving a control preserves
// its identity, permissions and listeners. No page content becomes HTML.
export function createPageHeader(doc, {title, headingId, description, identity, actions, className = 'toolbar'} = {}) {
  const header = doc.createElement('div');
  header.className = className; header.classList.add('page-header');
  if (!identity) {
    identity = doc.createElement('div');
    const heading = doc.createElement('h1'); heading.textContent = title;
    if (headingId) heading.id = headingId;
    identity.append(heading);
    if (description) {
      const text = doc.createElement('p'); text.className = 'page-description'; text.textContent = description;
      identity.append(text);
    }
  }
  identity.classList.add('page-header-copy');
  header.append(identity);
  if (actions) { actions.classList.add('page-actions'); header.append(actions); }
  return header;
}

// Static pages declare their content and actions; the same composition used
// by dynamic pages supplies the wrapper. Safe to call again after a mount.
export function enhancePageHeaders(root) {
  for (const host of root.querySelectorAll('[data-page-header]:not([data-page-header-ready])')) {
    const focused = host.contains(host.ownerDocument.activeElement) ? host.ownerDocument.activeElement : null;
    const actions = host.querySelector(':scope > .page-actions');
    let identity = host.querySelector(':scope > [data-page-identity]');
    if (!identity) {
      identity = host.ownerDocument.createElement('div');
      for (const child of [...host.childNodes]) if (child !== actions) identity.append(child);
    }
    const header = createPageHeader(host.ownerDocument, {identity, actions, className:host.className});
    host.replaceChildren(...header.childNodes);
    host.classList.add('page-header'); host.dataset.pageHeaderReady = '';
    focused?.focus({preventScroll:true});
  }
}

export function createFilterBar(doc, {label, controls = [], className = ''} = {}) {
  const bar = doc.createElement('div'); bar.className = `filter-bar ${className}`.trim();
  bar.setAttribute('role','search'); bar.setAttribute('aria-label',label);
  bar.append(...controls); return bar;
}

// Instant inventories and submitted server queries share binding/reset/focus
// behavior. The page retains query validation, persistence and data ownership.
export function bindFilterBar({root, fields, clearButtons = [], resetFields = Object.keys(fields), onChange, onApply, focus = Object.values(fields)[0]}) {
  const controls = new Set(Object.values(fields));
  const defaults = new Map([...controls].map(control => [control,control.tagName === 'SELECT'
    ? [...control.options].find(option => option.defaultSelected)?.value ?? control.options[0]?.value ?? '' : control.defaultValue]));
  const read = () => Object.fromEntries(Object.entries(fields).map(([name,control]) => [name,control.value]));
  const changed = event => onChange?.(read(),event);
  const submit = event => { event.preventDefault(); onApply?.(read(),event); };
  const reset = () => {
    for (const name of resetFields) { const control = fields[name]; control.value = defaults.get(control); }
    onChange?.(read()); focus?.focus();
  };
  const eventFor = control => control.type === 'search' ? 'input' : 'change';
  for (const control of controls) control.addEventListener(eventFor(control),changed);
  if (onApply) root.addEventListener('submit',submit);
  for (const button of clearButtons) button.addEventListener('click',reset);
  return {read,reset,dispose() {
    for (const control of controls) control.removeEventListener(eventFor(control),changed);
    if (onApply) root.removeEventListener('submit',submit);
    for (const button of clearButtons) button.removeEventListener('click',reset);
  }};
}

export function setFeedback(node, message, {error = false, role = error ? 'alert' : 'status', collapse = true} = {}) {
  node.setAttribute('role',role); node.setAttribute('aria-atomic','true');
  node.classList.toggle('is-error',error && !!message);
  node.textContent = message || ''; node.hidden = collapse && !message;
}

// These nodes stay mounted. Retaining a loaded result on failure must also
// retain its controls, disclosures and focus; this component never rebuilds it.
export function createListState({content, loading, error, empty, noMatches, busy = content}) {
  const hidden = (node,value) => { if(node) node.hidden = value; };
  function finish() { hidden(loading,true); busy?.removeAttribute('aria-busy'); }
  function results({total, visible = total}) {
    hidden(content,visible === 0); hidden(empty,total !== 0); hidden(noMatches,total === 0 || visible !== 0);
  }
  return {
    results, finish,
    pending({retain = false, message, clearError = false} = {}) {
      // A retry has not recovered yet. Keep the previous failure visible until
      // the page has received a successful response, unless it opts to clear it.
      if(error && clearError) setFeedback(error,'',{error:true});
      busy?.setAttribute('aria-busy','true');
      if(message !== undefined && loading) loading.textContent = message;
      hidden(loading,retain);
      if(!retain) { hidden(content,true); hidden(empty,true); hidden(noMatches,true); }
    },
    fail(message,{retain = false} = {}) {
      finish();
      if(!retain) { hidden(content,true); hidden(empty,true); hidden(noMatches,true); }
      if(error) setFeedback(error,message,{error:true});
    },
    clear() {
      finish(); hidden(content,true); hidden(empty,true); hidden(noMatches,true);
      if(error) setFeedback(error,'',{error:true});
    },
  };
}
