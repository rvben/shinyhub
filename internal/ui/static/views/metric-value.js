// Preserve the readable value while giving its number and unit distinct roles.
export function setMetricValue(element, text) {
  const value = String(text);
  if (element.textContent === value && element.querySelector('.metric-number')) return;
  const match = value.match(/^([<>]?\d[\d.,]*)(.*)$/s);
  if (!match) { element.textContent = value; return; }
  const number = element.ownerDocument.createElement('span');
  number.className = 'metric-number'; number.textContent = match[1];
  element.replaceChildren(number);
  if (match[2]) {
    const unit = element.ownerDocument.createElement('span');
    unit.className = 'metric-unit'; unit.textContent = match[2];
    element.append(unit);
  }
}
