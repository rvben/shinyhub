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
