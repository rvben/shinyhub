// GROUP_ORDER_FIXTURE is the single input both the grid grouper and the
// Launchpad model are asserted against, so "the two views agree on group order"
// is a claim about one concrete input rather than two similar ones.
//
// Deliberately arranged so a sort applied ACROSS groups produces a visibly
// different result from one applied within each group: name order (Alpha, Mike,
// Zulu) and deploy order (old-a, loose, new-b) both cut across the groups.
export const GROUP_ORDER_FIXTURE = [
  { slug: 'old-a', name: 'Zulu', project_slug: 'aaa', project_name: 'Aaa', last_deployed_at: '2020-01-01T00:00:00Z', status: 'running', deploy_count: 1 },
  { slug: 'new-b', name: 'Alpha', project_slug: 'bbb', project_name: 'Bbb', last_deployed_at: '2030-01-01T00:00:00Z', status: 'crashed', deploy_count: 1 },
  { slug: 'loose', name: 'Mike', last_deployed_at: '2025-01-01T00:00:00Z', status: 'stopped', deploy_count: 1 },
];

// The order every consumer must produce. Written out rather than derived, so a
// change to the shared rule fails here loudly instead of silently agreeing with
// itself.
export const GROUP_ORDER_EXPECTED = ['', 'aaa', 'bbb'];
