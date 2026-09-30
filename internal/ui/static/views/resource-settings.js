// Preserve settings changed by another operator while this form was open.
export function resourceSettingsPatch(current, baseline) {
  return Object.fromEntries(Object.entries(current).filter(([key, value]) => value !== baseline[key]));
}
