// The release strip is the one place on the app detail page that says what is
// live: the release number, when it went live (exact time plus age), what
// deployed it, and how many deploys the app has had. The header, the overview
// and the provenance row used to repeat parts of this; keeping it in one pure
// model and one renderer stops them drifting apart again.
import { deploymentTimeModel, provenanceModel } from './deployment-row.js';

export function releaseStripModel(app, now = Date.now()) {
  if (!app || !app.deploy_count) return null;
  const provenance = provenanceModel(app.deployment_provenance);
  const count = Number(app.deploy_count);
  return {
    versionLabel: app.release_number ? `v${app.release_number}` : '',
    versionTitle: app.released_version ? `bundle ${app.released_version}` : '',
    verb: provenance.verb,
    time: deploymentTimeModel(app.released_at || app.last_deployed_at, now),
    provenance,
    countLabel: `${count} deploy${count === 1 ? '' : 's'}`,
    historyHref: `/apps/${encodeURIComponent(app.slug)}/deployments`,
  };
}

function externalLink(doc, label, href) {
  const a = doc.createElement('a');
  a.href = href;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  a.textContent = label;
  return a;
}

function sourceGroup(doc, provenance, markFor) {
  if (!provenance.available) return null;
  const group = doc.createElement('span');
  group.className = 'release-source';
  const mark = markFor ? markFor(provenance) : null;
  if (mark) {
    mark.setAttribute('aria-hidden', 'true');
    group.append(mark);
  }
  if (provenance.summary) {
    group.append(provenance.summary);
    return group;
  }
  // CI and fleet deploys: the pipeline label is the link, followed by the
  // revision and the change request that produced it.
  group.append('by ');
  group.append(provenance.url ? externalLink(doc, provenance.label, provenance.url) : provenance.label);
  if (provenance.detail) {
    const revision = doc.createElement('span');
    revision.className = 'release-revision';
    revision.textContent = provenance.detail;
    group.append(' ', revision);
  }
  if (provenance.change) {
    group.append(' · ');
    group.append(provenance.change.url
      ? externalLink(doc, provenance.change.label, provenance.change.url)
      : provenance.change.label);
  }
  return group;
}

// renderReleaseStrip fills host from a releaseStripModel. A background refresh
// calls it with an unchanged model; it then leaves the DOM alone so a visitor
// selecting the timestamp or commit does not lose the selection.
export function renderReleaseStrip(doc, host, model, { markFor } = {}) {
  if (!host) return;
  const key = model ? JSON.stringify(model) : '';
  if (host.dataset.releaseKey === key && host.hidden === !model) return;
  host.dataset.releaseKey = key;
  host.hidden = !model;
  host.replaceChildren();
  if (!model) return;

  if (model.versionLabel) {
    const version = doc.createElement('span');
    version.className = 'release-version';
    version.textContent = model.versionLabel;
    if (model.versionTitle) version.title = model.versionTitle;
    host.append(version);
  }

  const summary = doc.createElement('p');
  summary.className = 'release-summary';
  summary.append(model.verb);
  if (model.time) {
    const time = doc.createElement('time');
    time.dateTime = model.time.datetime;
    time.textContent = model.time.absolute;
    const age = doc.createElement('span');
    age.className = 'release-age';
    age.textContent = `(${model.time.relative})`;
    summary.append(' ', time, ' ', age);
  }
  const source = sourceGroup(doc, model.provenance, markFor);
  if (source) summary.append(' ', source);
  host.append(summary);

  const history = doc.createElement('a');
  history.className = 'release-history';
  history.href = model.historyHref;
  history.setAttribute('data-nav', '');
  history.textContent = `${model.countLabel} · History`;
  host.append(history);
}
