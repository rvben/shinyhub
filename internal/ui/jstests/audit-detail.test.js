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

test('audit detail ignores malformed, unrelated, and unapproved fields', () => {
	assert.deepEqual(auditDetailEntries({action: 'schedule_activation_outcome', detail: '{bad'}), []);
	assert.deepEqual(auditDetailEntries({action: 'env.set', detail: '{"secret":"value"}'}), []);
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
