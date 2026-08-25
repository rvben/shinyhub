// Operator branding: applies window.__SHINYHUB_BRANDING__ (injected into the
// shell by internal/ui/branding.go RenderIndex) to the static markup.
//
// Every `.brand` node in the shell is a brand slot: the sidebar, the mobile top
// bar, the boot splash, and the login card. A slot carries the stock Orbit Hub
// lockup until an operator overrides it, so a self-hosted install shows its
// own identity everywhere the product shows ours - the login card included,
// which is the only chrome an anonymous visitor ever sees.
//
// Precedence per slot: logo image > site title text > stock lockup.
//
// DOM-free by default: applyBranding takes an explicit document so it is
// unit-testable with jsdom (jstests/branding.test.js). app.js owns the wiring.

// brandingIntent maps a branding object to what should be applied. Pure, so the
// precedence rules are testable without a DOM.
export function brandingIntent(branding) {
  const b = branding && typeof branding === 'object' ? branding : {};
  const logo = typeof b.logo === 'string' && b.logo.trim() ? b.logo.trim() : '';
  const siteTitle = typeof b.site_title === 'string' && b.site_title.trim() ? b.site_title.trim() : '';
  const links = Array.isArray(b.footer_links)
    ? b.footer_links.filter((l) => l && typeof l.url === 'string' && typeof l.label === 'string')
    : [];
  return {
    logo,
    siteTitle,
    // The image carries the site's name once it replaces the wordmark, so it
    // needs the name as its accessible text - never a generic "Home".
    logoAlt: siteTitle || 'ShinyHub',
    // Text override only when there is no logo: an operator with both gets the
    // image, and the title still reaches the <title> tag server-side.
    brandText: logo ? '' : siteTitle,
    footerLinks: links,
  };
}

function renderLogo(doc, slot, src, alt) {
  const img = doc.createElement('img');
  img.src = src;
  img.alt = alt;
  img.className = 'brand-logo';
  slot.replaceChildren(img);
}

function renderSiteTitle(doc, slot, text) {
  const name = doc.createElement('span');
  name.className = 'brand-name';
  name.textContent = text;
  // A title-only override is the operator's complete identity. Keeping the
  // stock Orbit Hub mark beside it would leak ShinyHub into a white-label.
  slot.replaceChildren(name);
}

function renderFooter(doc, links) {
  let footer = doc.querySelector('footer.brand-footer');
  if (!footer) {
    footer = doc.createElement('footer');
    footer.className = 'brand-footer';
    doc.body.appendChild(footer);
  }
  footer.replaceChildren(...links.map((link) => {
    const a = doc.createElement('a');
    a.href = link.url;
    a.textContent = link.label;
    a.rel = 'noopener noreferrer';
    return a;
  }));
}

// applyBranding writes the branding into `doc`. Returns the resolved intent so
// callers and tests can assert what was applied. A missing or empty branding
// object leaves the stock shell untouched.
export function applyBranding(doc, branding) {
  const intent = brandingIntent(branding);
  const slots = doc.querySelectorAll('.brand');
  for (const slot of slots) {
    if (slot.classList.contains('brand-home')) {
      slot.setAttribute('aria-label', `${intent.siteTitle || 'ShinyHub'} home`);
    }
    if (intent.logo) {
      renderLogo(doc, slot, intent.logo, intent.logoAlt);
    } else if (intent.brandText) {
      renderSiteTitle(doc, slot, intent.brandText);
    }
  }
  if (intent.footerLinks.length) renderFooter(doc, intent.footerLinks);
  return intent;
}
