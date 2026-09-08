import { test } from 'node:test';
import assert from 'node:assert/strict';
import { auditDetailEntries } from '../static/views/audit-detail.js';

test('activation outcome audit exposes its causal fields in a stable order', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'schedule_activation_outcome',
		detail: JSON.stringify({
			activation_id: 9,
			schedule_name: 'refresh',
			schedule_run_id: 42,
			target_generation: 7,
			status: 'failed',
			phase: 'starting_slot',
			error: 'health check failed',
		}),
	}), [
		{label: 'Activation', value: '9'},
		{label: 'Schedule', value: 'refresh'},
		{label: 'Source run', value: '42'},
		{label: 'Generation', value: '7'},
		{label: 'Outcome', value: 'failed'},
		{label: 'Last phase', value: 'starting_slot'},
		{label: 'Error', value: 'health check failed'},
	]);
});

test('malformed activation-outcome detail yields no entries rather than throwing', () => {
	assert.deepEqual(auditDetailEntries({action: 'schedule_activation_outcome', detail: '{bad'}), []);
});

test('activation outcome ignores fields outside its fixed set', () => {
	const entries = auditDetailEntries({
		action: 'schedule_activation_outcome',
		detail: '{"status":"succeeded","unexpected":"<script>"}',
	});
	assert.deepEqual(entries, [{label: 'Outcome', value: 'succeeded'}]);
});

test('support lifecycle exposes durable attribution without disclosing credentials', () => {
  for (const action of ['support_session.start', 'support_session.stop']) {
    const fields = auditDetailEntries({ action, resource_id:'session-42', created_at:'2026-09-07T09:00:00Z', detail:JSON.stringify({
      actor_username:'admin', subject_username:'alice', subject_user_id:42,
      app_slug:'sales', reason:'Investigating <script> as text', expires_at:'2026-09-07T09:15:00Z',
      stopped_at:action.endsWith('stop') ? '2026-09-07T09:03:00Z' : undefined,
      stop_reason:action.endsWith('stop') ? 'ended_by_actor' : undefined,
      launch_code:'do-not-render', token:'do-not-render',
    }) });
    const values = Object.fromEntries(fields.map(({label,value})=>[label,value]));
    assert.equal(values.Administrator,'admin');
    assert.equal(values['Represented user'],'alice');
    assert.equal(values.App,'sales');
    assert.equal(values.Reason,'Investigating <script> as text');
    assert.equal(values['Session ID'],'session-42');
    assert.ok(values.Deadline);
    if (action.endsWith('stop')) {
      assert.equal(values['End cause'],'Ended in app');
      assert.ok(values.Ended);
    }
    assert.doesNotMatch(JSON.stringify(fields),/do-not-render/);
  }
  assert.deepEqual(auditDetailEntries({action:'support_session.start', detail:'{"reason":{"secret":"hidden"}}'}), []);
});

// F7: every other action's detail used to be dropped outright (the Details
// column showed a dash) because only schedule_activation_outcome had an
// approved field set, even though the server recorded real who/what-changed
// data for almost every action. Real detail payloads never carry secret
// material (env.set stores {key, secret: bool, action}, never the value
// itself; see internal/api/env.go), so a generic object dump is safe.
test('a flat JSON detail object renders every field generically', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'env.set',
		detail: JSON.stringify({key: 'API_KEY', secret: true, action: 'created'}),
	}), [
		{label: 'Key', value: 'API_KEY'},
		{label: 'Secret', value: 'true'},
		{label: 'Action', value: 'created'},
	]);
});

test('an {old,new} field renders as a before -> after transition', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'update_user',
		detail: JSON.stringify({old_role: 'viewer', new_role: 'operator'}),
	}), [{label: 'Old role', value: 'viewer'}, {label: 'New role', value: 'operator'}]);
	// update_app nests {old, new} under the changed field's own key.
	assert.deepEqual(auditDetailEntries({
		action: 'update_app',
		detail: JSON.stringify({memory_limit_mb: {old: 256, new: 512}}),
	}), [{label: 'Memory limit mb', value: '256 → 512'}]);
});

test('a plain "key=value" detail string parses into fields', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'grant_access',
		detail: 'user_id=4 role=manager',
	}), [{label: 'User id', value: '4'}, {label: 'Role', value: 'manager'}]);
});

test('free-form text detail that fits neither shape renders verbatim', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'deploy_rejected_quota',
		detail: 'used=104857600 bytes, quota=500 MiB',
	}), [{label: 'Detail', value: 'used=104857600 bytes, quota=500 MiB'}]);
});

test('missing or empty detail renders no entries', () => {
	assert.deepEqual(auditDetailEntries({action: 'login', detail: ''}), []);
	assert.deepEqual(auditDetailEntries({action: 'login', detail: null}), []);
	assert.deepEqual(auditDetailEntries(null), []);
});

// db.AuditDetail (internal/db/audit_detail.go) replaces the whole detail
// object with {detail_error: "..."} when it cannot encode what it meant to
// record. That must render as a flagged problem, not blend in as one more
// ordinary field.
test('a detail_error object renders as a single flagged entry', () => {
	assert.deepEqual(auditDetailEntries({
		action: 'update_app',
		detail: JSON.stringify({detail_error: 'json: unsupported type: chan int'}),
	}), [{label: 'Detail unavailable', value: 'json: unsupported type: chan int', isError: true}]);
});
