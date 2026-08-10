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
//
// A null or undefined entry means the server had no measurement for that
// instant. Such a point is left out of the line and out of the min/max scale, so
// the stroke joins its neighbours straight across the gap while every remaining
// point keeps the x position its index earns. Plotting it as 0 instead would
// draw a dip to the floor, which on a CPU series is indistinguishable from the
// app going idle and lands exactly on restarts, when the first sample of a new
// process has no rate yet. Other non-finite values (NaN, Infinity) are malformed
// rather than absent and still collapse to 0.
export function sparklinePoints(values, opts = {}) {
  const { width = 120, height = 28, step = false } = opts;
  const vals = values.map((v) =>
    v === null || v === undefined ? null : Number.isFinite(v) ? v : 0,
  );
  const n = vals.length;
  if (n === 0) return [];
  if (n === 1) return vals[0] === null ? [] : [`0,${fmt(height / 2)}`];

  const drawn = [];
  for (let i = 0; i < n; i++) {
    if (vals[i] !== null) drawn.push(i);
  }
  if (drawn.length === 0) return [];

  const stepX = width / (n - 1);
  if (drawn.length === 1) return [`${fmt(drawn[0] * stepX)},${fmt(height / 2)}`];

  let min = Infinity;
  let max = -Infinity;
  for (const i of drawn) {
    if (vals[i] < min) min = vals[i];
    if (vals[i] > max) max = vals[i];
  }
  const span = max - min;
  const coord = (i) => {
    const x = i * stepX;
    const y = span === 0 ? height / 2 : height - ((vals[i] - min) / span) * height;
    return [x, y];
  };

  const out = [];
  for (let k = 0; k < drawn.length; k++) {
    const [x, y] = coord(drawn[k]);
    if (step && k > 0) {
      const [, yPrev] = coord(drawn[k - 1]);
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
