// emoji-picker.js - the curated emoji list and DOM-free model backing the
// Configuration -> General "Choose emoji" control (Task 10). The server
// (deploy.ValidateIconEmoji) is the authority on which emoji are valid; the
// check here is deliberately coarse (non-empty, one grapheme) rather than a
// hand-copied port of the server's Unicode range tables, which would drift
// silently. A curated entry that the server would reject is instead caught
// by a Go contract test that runs every CURATED_EMOJI literal through the
// real validator (internal/ui/contract_test.go).

// CURATED_EMOJI covers charts/data, science, ops/infra, and generic-app
// glyphs. Each entry's name becomes its cell's aria-label, so keep names
// short and descriptive.
export const CURATED_EMOJI = [
  { emoji: '📊', name: 'Bar chart' },
  { emoji: '📈', name: 'Trending up' },
  { emoji: '📉', name: 'Trending down' },
  { emoji: '🧮', name: 'Abacus' },
  { emoji: '📐', name: 'Triangular ruler' },
  { emoji: '🗂️', name: 'Card index dividers' },
  { emoji: '🗄️', name: 'File cabinet' },
  { emoji: '📇', name: 'Card index' },
  { emoji: '🧾', name: 'Receipt' },
  { emoji: '📋', name: 'Clipboard' },
  { emoji: '🔬', name: 'Microscope' },
  { emoji: '🧪', name: 'Test tube' },
  { emoji: '🧬', name: 'DNA' },
  { emoji: '⚗️', name: 'Alembic' },
  { emoji: '🌡️', name: 'Thermometer' },
  { emoji: '☢️', name: 'Radioactive' },
  { emoji: '🧲', name: 'Magnet' },
  { emoji: '🔭', name: 'Telescope' },
  { emoji: '⚙️', name: 'Gear' },
  { emoji: '🛠️', name: 'Hammer and wrench' },
  { emoji: '🔧', name: 'Wrench' },
  { emoji: '🖥️', name: 'Desktop computer' },
  { emoji: '💻', name: 'Laptop' },
  { emoji: '⚡', name: 'High voltage' },
  { emoji: '🔌', name: 'Electric plug' },
  { emoji: '🔋', name: 'Battery' },
  { emoji: '📡', name: 'Satellite antenna' },
  { emoji: '🛡️', name: 'Shield' },
  { emoji: '🔐', name: 'Locked with key' },
  { emoji: '🔑', name: 'Key' },
  { emoji: '🧰', name: 'Toolbox' },
  { emoji: '🪛', name: 'Screwdriver' },
  { emoji: '🚀', name: 'Rocket' },
  { emoji: '🧭', name: 'Compass' },
  { emoji: '🗺️', name: 'World map' },
  { emoji: '🌐', name: 'Globe with meridians' },
  { emoji: '🔗', name: 'Link' },
  { emoji: '🧑‍💻', name: 'Coder' },
  { emoji: '📦', name: 'Package' },
  { emoji: '🏷️', name: 'Label' },
  { emoji: '🔖', name: 'Bookmark' },
  { emoji: '📌', name: 'Pushpin' },
  { emoji: '📅', name: 'Calendar' },
  { emoji: '⏰', name: 'Alarm clock' },
  { emoji: '⏳', name: 'Hourglass' },
  { emoji: '🔔', name: 'Bell' },
  { emoji: '📣', name: 'Megaphone' },
  { emoji: '💡', name: 'Light bulb' },
  { emoji: '🎯', name: 'Direct hit' },
  { emoji: '🧩', name: 'Puzzle piece' },
  { emoji: '🕹️', name: 'Joystick' },
  { emoji: '📝', name: 'Memo' },
  { emoji: '✅', name: 'Check mark' },
  { emoji: '🌟', name: 'Glowing star' },
  { emoji: '🔥', name: 'Fire' },
  { emoji: '🌊', name: 'Water wave' },
  { emoji: '🌱', name: 'Seedling' },
  { emoji: '🌳', name: 'Deciduous tree' },
  { emoji: '☀️', name: 'Sun' },
  { emoji: '☁️', name: 'Cloud' },
  { emoji: '💰', name: 'Money bag' },
  { emoji: '💳', name: 'Credit card' },
  { emoji: '🧠', name: 'Brain' },
  { emoji: '🦠', name: 'Microbe' },
];

// graphemeCount counts user-perceived characters, not UTF-16 units or code
// points, so a ZWJ sequence (e.g. "coder") or a flag counts as one glyph.
// Intl.Segmenter is available in every browser this dashboard targets; the
// Array.from fallback (code-point iteration) is coarser but never throws.
function graphemeCount(value) {
  if (typeof Intl !== 'undefined' && typeof Intl.Segmenter === 'function') {
    const segmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' });
    let count = 0;
    // eslint-disable-next-line no-unused-vars
    for (const _ of segmenter.segment(value)) count++;
    return count;
  }
  return Array.from(value).length;
}

// isSingleEmoji is the client-side pre-check before a PATCH: non-empty and
// exactly one grapheme. It does not verify the glyph is actually an emoji
// (that needs the server's Unicode range tables); it only blocks the empty
// string, plain text, and an accidental multi-emoji paste before a round
// trip. The server (deploy.ValidateIconEmoji) has the final say.
export function isSingleEmoji(value) {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed) return false;
  return graphemeCount(trimmed) === 1;
}

// nextEmojiIndex resolves arrow-key navigation across the picker grid. Like
// the tablist model (views/tablist-keys.js), Right/Down move forward and
// Left/Up move back with wraparound; Home/End jump to the ends. current -1
// (nothing focused yet) starts at the first cell on Right/Down and the last
// on Left/Up.
export function nextEmojiIndex(count, current, key) {
  if (count <= 0) return -1;
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return current < 0 ? 0 : (current + 1) % count;
    case 'ArrowLeft':
    case 'ArrowUp':
      return current < 0 ? count - 1 : (current - 1 + count) % count;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    default:
      return -1;
  }
}

// renderEmojiPicker builds the grid once: one real <button> per curated
// entry (aria-label = entry name, so screen readers announce "Rocket", not
// the raw glyph), with roving-tabindex arrow-key navigation. Takes an
// explicit document so it is unit-testable with jsdom.
export function renderEmojiPicker(doc, { onPick } = {}) {
  const grid = doc.createElement('div');
  grid.className = 'emoji-picker-grid';
  const buttons = CURATED_EMOJI.map((entry, i) => {
    const btn = doc.createElement('button');
    btn.type = 'button';
    btn.className = 'emoji-picker-cell';
    btn.textContent = entry.emoji;
    btn.setAttribute('aria-label', entry.name);
    btn.tabIndex = i === 0 ? 0 : -1;
    btn.addEventListener('click', () => {
      if (typeof onPick === 'function') onPick(entry.emoji);
    });
    grid.appendChild(btn);
    return btn;
  });
  grid.addEventListener('keydown', (e) => {
    const current = buttons.indexOf(doc.activeElement);
    const dest = nextEmojiIndex(buttons.length, current, e.key);
    if (dest === -1) return;
    e.preventDefault();
    buttons.forEach((btn, i) => { btn.tabIndex = i === dest ? 0 : -1; });
    buttons[dest].focus();
  });
  return grid;
}
