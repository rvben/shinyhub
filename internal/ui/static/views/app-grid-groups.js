// app-grid-groups.js - grouping for the operator dashboard grid. Grouping is
// applied OUTSIDE the existing sort control: search and segment filters run in
// app.js as before, the survivors are partitioned by project, and the chosen
// sort key orders the cards WITHIN each group. The sort key never reorders the
// groups themselves - ordering sections by "most recent deploy" would make
// headings jump between renders, which defeats the purpose of an index.
import { groupApps } from './project-groups.js';

const STATUS_ORDER = { crashed: 0, running: 1, stopped: 2, failed: 3 };

const deployedAt = (a) => (a.last_deployed_at ? new Date(a.last_deployed_at).getTime() : 0);

/**
 * gridSortComparator returns the in-group comparator for one #apps-sort value,
 * or null for "keep server order". These are the dashboard's existing
 * comparators, moved here unchanged so grouping cannot alter what a sort means.
 * @returns {?function}
 */
export function gridSortComparator(sortKey) {
  switch (sortKey) {
    case 'name':
      return (a, b) => a.name.localeCompare(b.name);
    case 'deploy':
      return (a, b) => deployedAt(b) - deployedAt(a);
    case 'status':
      return (a, b) => (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9);
    default:
      // 'default' and any unrecognized value keep server order.
      return null;
  }
}

/**
 * groupAppsForGrid groups the already-filtered apps for the dashboard grid.
 * @param {Array<object>} apps  filtered apps, in server order
 * @param {{sortKey?:string, projects?:Array<object>}} [opts]
 * @returns {Array<{project:string,name:string,iconEmoji:string,apps:Array<object>}>}
 */
export function groupAppsForGrid(apps, opts) {
  const o = opts || {};
  return groupApps(apps, { projects: o.projects, sortWithin: gridSortComparator(o.sortKey) });
}
