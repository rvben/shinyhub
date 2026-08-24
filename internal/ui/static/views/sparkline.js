// Pure SVG sparkline renderer for the app detail "Trends" card. DOM-free except
// for an injected `document`, so it unit-tests under jsdom and embeds with no
// build step. SVG (not canvas) keeps it crisp on HiDPI for free, themeable via
// `currentColor`, and accessible through a caller-supplied chart description.

const SVG_NS = 'http://www.w3.org/2000/svg';

// fmt renders a coordinate compactly: at most two decimals, trailing zeros
// stripped (so 50 -> "50", 10.5 -> "10.5").
function fmt(n) {
  return String(Number(n.toFixed(2)));
}

// sparklineSegments maps a numeric series to contiguous groups of "x,y" points
// scaled into a width x height box (min at the bottom, max at the top). A single
// point or a flat series (all equal) renders along the vertical middle unless an
// explicit domain supplies an honest position. With `step: true` it emits a
// stepped path for discrete signals like instance count.
//
// A null or undefined entry means the server had no measurement for that
// instant. It is excluded from the scale and splits the stroke, while every
// surviving point keeps the x position its timestamp earns. Plotting it as 0
// would invent a dip; joining across it would invent continuity. Other
// non-finite values (NaN, Infinity) are malformed rather than absent and still
// collapse to 0.
export function sparklineSegments(values, opts = {}) {
  const {
    width = 120,
    height = 28,
    step = false,
    domainMin,
    domainMax,
  } = opts;
  const vals = values.map((v) =>
    v === null || v === undefined ? null : Number.isFinite(v) ? v : 0,
  );
  const n = vals.length;
  if (n === 0) return [];

  const drawn = [];
  for (let i = 0; i < n; i++) {
    if (vals[i] !== null) drawn.push(i);
  }
  if (drawn.length === 0) return [];

  const stepX = n > 1 ? width / (n - 1) : 0;

  let min = Infinity;
  let max = -Infinity;
  for (const i of drawn) {
    if (vals[i] < min) min = vals[i];
    if (vals[i] > max) max = vals[i];
  }
  if (Number.isFinite(domainMin)) min = domainMin;
  if (Number.isFinite(domainMax)) max = domainMax;
  const span = max - min;
  const coord = (i) => {
    const x = i * stepX;
    const y = span === 0 ? height / 2 : height - ((vals[i] - min) / span) * height;
    return [x, y];
  };

  const segments = [];
  let segment = [];
  for (let i = 0; i < vals.length; i++) {
    if (vals[i] === null) {
      if (segment.length > 0) segments.push(segment);
      segment = [];
      continue;
    }

    const [x, y] = coord(i);
    if (step && segment.length > 0) {
      const [, yPrev] = coord(i - 1);
      segment.push(`${fmt(x)},${fmt(yPrev)}`);
    }
    segment.push(`${fmt(x)},${fmt(y)}`);
  }
  if (segment.length > 0) segments.push(segment);
  return segments;
}

// sparklinePoints remains the compact coordinate helper used by callers that do
// not need the segment boundaries. renderSparkline uses the boundaries so gaps
// are visible rather than bridged.
export function sparklinePoints(values, opts = {}) {
  return sparklineSegments(values, opts).flat();
}

// renderSparkline returns an <svg> element drawing `values` as a sparkline. An
// empty series yields an <svg> with no polyline so the caller can still place it
// (the caller decides whether to show "collecting..." instead). Options include
// dimensions, step mode, an explicit domain, grid lines, an endpoint marker,
// an accessible label, and a CSS class.
export function renderSparkline(document, values, opts = {}) {
  const {
    width = 120,
    height = 28,
    step = false,
    ariaLabel = '',
    className = 'sparkline',
    domainMin,
    domainMax,
    grid = false,
    endPoint = false,
  } = opts;
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('class', className);
  svg.setAttribute('role', 'img');
  if (ariaLabel) svg.setAttribute('aria-label', ariaLabel);

  if (grid) {
    for (const y of [0.5, height / 2, height - 0.5]) {
      const line = document.createElementNS(SVG_NS, 'line');
      line.setAttribute('class', 'sparkline-grid');
      line.setAttribute('x1', '0');
      line.setAttribute('x2', String(width));
      line.setAttribute('y1', String(y));
      line.setAttribute('y2', String(y));
      line.setAttribute('vector-effect', 'non-scaling-stroke');
      svg.appendChild(line);
    }
  }

  const segments = sparklineSegments(values, { width, height, step, domainMin, domainMax });
  for (const pts of segments) {
    if (pts.length === 1) {
      const [x, y] = pts[0].split(',');
      const sample = document.createElementNS(SVG_NS, 'circle');
      sample.setAttribute('class', 'sparkline-sample');
      sample.setAttribute('cx', x);
      sample.setAttribute('cy', y);
      sample.setAttribute('r', '2');
      sample.setAttribute('vector-effect', 'non-scaling-stroke');
      svg.appendChild(sample);
      continue;
    }

    const poly = document.createElementNS(SVG_NS, 'polyline');
    poly.setAttribute('class', 'sparkline-series');
    poly.setAttribute('points', pts.join(' '));
    poly.setAttribute('fill', 'none');
    poly.setAttribute('stroke', 'currentColor');
    poly.setAttribute('stroke-width', '1.5');
    poly.setAttribute('vector-effect', 'non-scaling-stroke');
    poly.setAttribute('stroke-linejoin', 'round');
    poly.setAttribute('stroke-linecap', 'round');
    svg.appendChild(poly);
  }

  const latestIsPresent = values.length > 0 && values[values.length - 1] !== null
    && values[values.length - 1] !== undefined;
  if (endPoint && latestIsPresent && segments.length > 0) {
    const latestSegment = segments[segments.length - 1];
    const [x, y] = latestSegment[latestSegment.length - 1].split(',');
    const dot = document.createElementNS(SVG_NS, 'circle');
    dot.setAttribute('class', 'sparkline-endpoint');
    dot.setAttribute('cx', x);
    dot.setAttribute('cy', y);
    dot.setAttribute('r', '2.5');
    dot.setAttribute('vector-effect', 'non-scaling-stroke');
    svg.appendChild(dot);
  }
  return svg;
}
