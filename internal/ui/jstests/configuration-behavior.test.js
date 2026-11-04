import test from 'node:test';
import assert from 'node:assert/strict';
import { savedConfigurationBehavior } from '../static/views/configuration-behavior.js';
const defaults = {isolation:'multiplex',sessions:20,target:0.75,hibernation:30};
const app = {replicas:2,autoscale_enabled:true,autoscale_min_replicas:1,autoscale_max_replicas:8,min_warm_replicas:0};
const envelope = {global_autoscale_enabled:true,effective_autoscale_max_replicas:4};

test('saved behavior combines autoscale with full idle hibernation', () => {
  const text = savedConfigurationBehavior(app,envelope,defaults);
  assert.match(text,/1 to 4 replicas while active/);
  assert.match(text,/Sleeps after 30 idle minutes; the next visitor wakes it/);
});
test('keep-warm raises the active floor but cannot exceed the effective ceiling', () => {
  const text = savedConfigurationBehavior({...app,min_warm_replicas:3},envelope,defaults);
  assert.match(text,/3 to 4 replicas/); assert.match(text,/keeps 3 replicas warm/);
  assert.match(savedConfigurationBehavior({...app,min_warm_replicas:10},envelope,defaults),/Autoscale: 4 replicas/);
});
test('never hibernate preserves the warm autoscale floor', () => {
  const text = savedConfigurationBehavior({...app,min_warm_replicas:2,hibernate_timeout_minutes:0},envelope,defaults);
  assert.match(text,/2 to 4 replicas/); assert.match(text,/hibernation is off/); assert.doesNotMatch(text,/Sleeps/);
});
test('server pause and an unlimited session cap do not promise active autoscaling', () => {
  assert.match(savedConfigurationBehavior(app,{...envelope,global_autoscale_enabled:false},defaults),/paused server-wide.*Manual setting: 2 replicas/);
  assert.match(savedConfigurationBehavior(app,envelope,{...defaults,sessions:0}),/needs a session cap/);
});
test('unknown effective settings remain unknown', () => {
  assert.match(savedConfigurationBehavior(app,{},defaults),/effective policy is unavailable/);
  assert.match(savedConfigurationBehavior(app,envelope,{...defaults,target:null}),/effective policy is unavailable/);
  assert.match(savedConfigurationBehavior({}, {}, {}),/not reported the worker policy/);
  assert.match(savedConfigurationBehavior({replicas:1},{},{isolation:'multiplex'}),/Idle timeout unavailable/);
});
test('elastic modes describe clients, warm worker capacity and inactive replica controls', () => {
  const text = savedConfigurationBehavior({...app,worker_isolation:'per_session',worker_max_workers:10,worker_warm_spares:2},envelope,defaults);
  assert.match(text,/One worker per browser client, with up to 10 workers/);
  assert.match(text,/2 warm workers stay ready for new clients and count toward that limit/);
  assert.match(text,/Replica autoscale and Keep warm do not control this pool/);
  assert.doesNotMatch(text,/idle minutes|1 to 4 replicas/);
  assert.match(savedConfigurationBehavior({worker_isolation:'grouped',worker_grouped_size:4,worker_max_workers:10},envelope,defaults),/4 browser clients share each worker/);
});

test('keep-warm explanation only claims an increase when the effective floor rises', () => {
  for (const bounds of [{autoscale_min_replicas:0,min_warm_replicas:1}, {autoscale_min_replicas:4,min_warm_replicas:10}]) {
    assert.doesNotMatch(savedConfigurationBehavior({...app,...bounds},envelope,defaults),/Keep warm raises/);
  }
});
test('elastic cold starts apply to new clients and the idle timeout remains visible', () => {
  const elastic = {worker_isolation:'per_session',worker_max_workers:10};
  assert.match(savedConfigurationBehavior(elastic,envelope,defaults),/a client that needs a new worker waits for it to start/);
  assert.match(savedConfigurationBehavior(elastic,envelope,defaults),/Idle timeout setting: 30 minutes/);
  assert.match(savedConfigurationBehavior({...elastic,hibernate_timeout_minutes:0},envelope,defaults),/Automatic hibernation is off/);
});
