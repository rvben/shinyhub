const fields = {
  replicas: 'scaling-replicas',
  max_sessions_per_replica: 'scaling-cap',
  worker_isolation: 'worker-isolation',
  worker_grouped_size: 'worker-grouped-size',
  worker_max_workers: 'worker-max-workers',
  worker_warm_spares: 'worker-warm-spares',
};

export function scalingSettingsSnapshot(doc) {
  return Object.fromEntries(Object.entries(fields).map(([key, id]) => {
    const value = doc.getElementById(id).value;
    return [key, key === 'worker_isolation' ? value : Number(value)];
  }));
}

// Compare against the displayed baseline, preserving inherited values and
// concurrent server-side scaling when the operator only changes the cap.
export function scalingSettingsPatch(current, baseline) {
  return Object.fromEntries(Object.entries(current).filter(([key, value]) => value !== baseline[key]));
}
