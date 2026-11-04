// jsdom has no top layer. Model only native state events here; browser QA
// verifies positioning, Escape, light dismissal and keyboard traversal.
export function installPopoverStub(window) {
  const setState = function (open) {
    const event = new window.Event('beforetoggle');
    Object.defineProperty(event, 'newState', { value: open ? 'open' : 'closed' });
    this.dispatchEvent(event);
    this.dataset.testPopoverOpen = String(open);
  };
  window.HTMLElement.prototype.showPopover = function () { setState.call(this, true); };
  window.HTMLElement.prototype.hidePopover = function () { setState.call(this, false); };
}
