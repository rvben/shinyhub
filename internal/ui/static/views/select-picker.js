// Customizable select pickers live in the top layer but still dispatch key
// events through the document. Let the browser dismiss the picker on Escape
// without also dismissing its parent dialog/drawer or losing unsaved input.
export function installSelectPickerGuard(doc) {
  if (!doc.defaultView?.CSS?.supports('appearance', 'base-select')) return () => {};
  const onKeydown = event => {
    if (event.key !== 'Escape') return;
    const select = event.target?.closest?.('select');
    if (select?.matches(':open')) event.stopPropagation();
    // Do not preventDefault: the browser still owns closing and focus return.
  };
  doc.addEventListener('keydown', onKeydown, true);
  return () => doc.removeEventListener('keydown', onKeydown, true);
}
