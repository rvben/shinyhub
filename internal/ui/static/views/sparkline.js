// Pure SVG sparkline renderer for the app detail "Trends" card. DOM-free except
// for an injected `document`, so it unit-tests under jsdom and embeds with no
// build step. SVG (not canvas) keeps it crisp on HiDPI for free, themeable via
// `currentColor`, and accessible via an aria-label carrying the current value.

const SVG_NS = 'http://www.w3.org/2000/svg';

// fmt renders a coordinate compactly: at most two decimals, trailing zeros
// stripped (so 50 -> "50", 10.5 -> "10.5").
function fmt(n) {
  return String(Number(n.toFixed(2)));
}

// sparklinePoints maps a numeric series to "x,y" polyline points scaled into a
// width x height box (min at the bottom, max at the top). Returns [] for an empty
// series. A single point or a flat series (all equal) renders along the vertical
// middle, avoiding a divide-by-zero and a misleading full-height line. With
// `step: true` it emits a stepped path (2n-1 points) for discrete signals like
// instance count.
export function sparklinePoints(values, opts = {}) {
  const { width = 120, height = 28, step = false } = opts;
  // Coerce non-finite values (NaN/Infinity/null) to 0 so a malformed point can
  // never poison the polyline with a literal "NaN".
  const vals = values.map((v) => (Number.isFinite(v) ? v : 0));
  const n = vals.length;
  if (n === 0) return [];
  if (n === 1) return [`0,${fmt(height / 2)}`];

  let min = Infinity;
  let max = -Infinity;
  for (const v of vals) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const span = max - min;
  const stepX = width / (n - 1);
  const coord = (i) => {
    const x = i * stepX;
    const y = span === 0 ? height / 2 : height - ((vals[i] - min) / span) * height;
    return [x, y];
  };

  const out = [];
  for (let i = 0; i < n; i++) {
    const [x, y] = coord(i);
    if (step && i > 0) {
      const [, yPrev] = coord(i - 1);
      out.push(`${fmt(x)},${fmt(yPrev)}`);
    }
    out.push(`${fmt(x)},${fmt(y)}`);
  }
  return out;
}

// renderSparkline returns an <svg> element drawing `values` as a sparkline. An
// empty series yields an <svg> with no polyline so the caller can still place it
// (the caller decides whether to show "collecting..." instead). Options:
// width, height, step, ariaLabel, className.
export function renderSparkline(document, values, opts = {}) {
  const { width = 120, height = 28, step = false, ariaLabel = '', className = 'sparkline' } = opts;
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('class', className);
  svg.setAttribute('role', 'img');
  if (ariaLabel) svg.setAttribute('aria-label', ariaLabel);

  const pts = sparklinePoints(values, { width, height, step });
  if (pts.length > 0) {
    const poly = document.createElementNS(SVG_NS, 'polyline');
    poly.setAttribute('points', pts.join(' '));
    poly.setAttribute('fill', 'none');
    poly.setAttribute('stroke', 'currentColor');
    poly.setAttribute('stroke-width', '1.5');
    poly.setAttribute('vector-effect', 'non-scaling-stroke');
    poly.setAttribute('stroke-linejoin', 'round');
    poly.setAttribute('stroke-linecap', 'round');
    svg.appendChild(poly);
  }
  return svg;
}
