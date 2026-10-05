import test from 'node:test';
import assert from 'node:assert/strict';
import { Visualizer, OFF, bars, scope, bandEdges, bandLevels, step, scopePoints } from '../../js/viz/index.js';

// ------------------------------------------------------------------ fakes for the browser bits

function fakeCanvas({ w = 200, h = 50, cssViz = '' } = {}) {
  const ops = [];
  const g = {
    ops,
    clearRect: (...a) => ops.push(['clear', ...a]),
    fillRect: (...a) => ops.push(['fill', ...a]),
    beginPath: () => ops.push(['begin']),
    moveTo: (...a) => ops.push(['move', ...a]),
    lineTo: (...a) => ops.push(['line', ...a]),
    stroke: () => ops.push(['stroke', g.strokeStyle, g.lineWidth]),
  };
  return { clientWidth: w, clientHeight: h, width: 0, height: 0, getContext: () => g, g, cssViz };
}

function fakeAnalyser({ level = 255, sampleRate = 48000 } = {}) {
  return {
    context: { sampleRate },
    frequencyBinCount: 1024,
    fftSize: 2048,
    getByteFrequencyData: (a) => a.fill(level),
    getByteTimeDomainData: (a) => a.forEach((_, i) => (a[i] = i % 2 ? 255 : 0)),
  };
}

// requestAnimationFrame that runs frames only when asked to.
function fakeFrames(t) {
  const saved = {
    raf: globalThis.requestAnimationFrame,
    caf: globalThis.cancelAnimationFrame,
    gcs: globalThis.getComputedStyle,
    dpr: globalThis.devicePixelRatio,
  };
  let queue = new Map();
  let id = 0;
  globalThis.requestAnimationFrame = (fn) => (queue.set(++id, fn), id);
  globalThis.cancelAnimationFrame = (n) => queue.delete(n);
  globalThis.getComputedStyle = (el) => ({ getPropertyValue: (k) => (k === '--viz' ? el.cssViz : '') });
  globalThis.devicePixelRatio = 2;
  t.after(() => {
    globalThis.requestAnimationFrame = saved.raf;
    globalThis.cancelAnimationFrame = saved.caf;
    globalThis.getComputedStyle = saved.gcs;
    globalThis.devicePixelRatio = saved.dpr;
  });
  return {
    pending: () => queue.size,
    run(time) {
      const due = queue;
      queue = new Map();
      for (const fn of due.values()) fn(time);
    },
  };
}

// ------------------------------------------------------------------ the maths

test('bandEdges are increasing, within range and span 80 Hz..12 kHz', () => {
  const edges = bandEdges(44100, 1024, 24);
  assert.equal(edges.length, 25);
  for (let i = 1; i < edges.length; i++) assert.ok(edges[i] >= edges[i - 1]);
  assert.ok(edges[0] >= 0 && edges[edges.length - 1] <= 1023);
  const hz = (bin) => (bin / 1023) * 22050;
  assert.ok(Math.abs(hz(edges[0]) - 80) < 30);
  assert.ok(Math.abs(hz(edges[24]) - 12000) < 30);
  // The range is configurable and capped at Nyquist.
  const low = bandEdges(16000, 1024, 4, 100, 20000);
  assert.equal(low.length, 5);
  assert.equal(low[4], 1023);
});

test('step rises fast, falls smoothly and settles at zero', () => {
  assert.ok(step(0, 100, 16.7) > 50);
  const fall = step(100, 0, 16.7);
  assert.ok(fall > 85 && fall < 95);
  assert.equal(step(0.5, 0, 16.7), 0);
  assert.equal(step(100, 95, 16.7), 95); // never undershoots the target
});

test('bandLevels averages, blends with neighbours and scales by square root', () => {
  const full = bandLevels(new Uint8Array(64).fill(255), [0, 8, 16, 32]);
  assert.deepEqual(full, [255, 255, 255]);
  assert.deepEqual(bandLevels(new Uint8Array(64), [0, 8, 16, 32]), [0, 0, 0]);
  // A quarter of full scale reads as half height.
  assert.ok(Math.abs(bandLevels(new Uint8Array(64).fill(64), [0, 8, 16])[0] - 255 / 2) < 1);
  // A lone loud band leaks into its neighbours.
  const freq = new Uint8Array(30);
  freq.fill(255, 10, 20);
  const [a, b, c] = bandLevels(freq, [0, 10, 20, 30]);
  assert.ok(b > a && a > 0 && a === c);
  // A band narrower than two bins still reads two (and never runs off the end).
  assert.equal(bandLevels(new Uint8Array([0, 255]), [0, 0, 1]).length, 2);
});

test('scopePoints keeps each column peak and centres silence', () => {
  assert.deepEqual(scopePoints(new Uint8Array(8).fill(128), 4, 100), [50, 50, 50, 50]);
  const ys = scopePoints(new Uint8Array([128, 0, 128, 255]), 2, 100, 1);
  assert.deepEqual(ys, [0, 50 + (127 / 128) * 50]);
  assert.equal(scopePoints(new Uint8Array([0]), 3, 10).length, 3); // fewer samples than columns
});

// ------------------------------------------------------------------ the core

test('the first mode is the default; modes are listed with off last', (t) => {
  fakeFrames(t);
  const viz = new Visualizer(fakeCanvas(), fakeAnalyser(), { modes: [bars(), scope()] });
  assert.equal(viz.mode, 'bars');
  assert.deepEqual(viz.modes, ['bars', 'scope', OFF]);
  assert.equal(new Visualizer(fakeCanvas(), fakeAnalyser()).mode, OFF);
  assert.equal(new Visualizer(fakeCanvas(), fakeAnalyser(), { modes: [bars()], mode: OFF }).mode, OFF);
  assert.throws(() => new Visualizer(fakeCanvas(), fakeAnalyser(), { modes: [bars()], mode: 'nope' }), RangeError);
});

test('register validates plugins and replaces one of the same name', (t) => {
  fakeFrames(t);
  const canvas = fakeCanvas();
  const viz = new Visualizer(canvas, fakeAnalyser());
  assert.throws(() => viz.register({ name: 'x' }), TypeError);
  assert.throws(() => viz.register({ create() {} }), TypeError);
  assert.throws(() => viz.register(null), TypeError);
  assert.throws(() => viz.register({ name: OFF, create() {} }), RangeError);
  let made = 0;
  const v1 = { name: 'x', create: () => (made++, { draw: ({ g }) => g.fillRect(1, 1, 1, 1) }) };
  const v2 = { name: 'x', create: () => (made++, { draw: ({ g }) => g.fillRect(2, 2, 2, 2) }) };
  viz.register(v1).setMode('x');
  viz.draw();
  viz.draw();
  assert.equal(made, 1, 'a renderer is created once and kept');
  viz.register(v2);
  viz.draw();
  assert.equal(made, 2, 'a replaced plugin gets a fresh renderer');
  assert.deepEqual(canvas.g.ops.at(-1), ['fill', 2, 2, 2, 2]);
  assert.throws(() => viz.setMode('nope'), RangeError);
});

test('a custom mode gets the analyser, the sample rate and a ready context', (t) => {
  fakeFrames(t);
  const canvas = fakeCanvas({ w: 100, h: 30, cssViz: ' #abc ' });
  const analyser = fakeAnalyser({ sampleRate: 22050 });
  let env;
  let frame;
  const dots = {
    name: 'dots',
    create(e) {
      env = e;
      return { draw: (f) => (frame = { ...f, fill: f.g.fillStyle }) };
    },
  };
  const viz = new Visualizer(canvas, analyser, { modes: [dots] });
  viz.draw(20);
  assert.equal(env.analyser, analyser);
  assert.equal(env.sampleRate, 22050);
  assert.equal(frame.width, 200, 'device pixels: CSS size times devicePixelRatio');
  assert.equal(frame.height, 60);
  assert.equal(frame.dpr, 2);
  assert.equal(frame.dt, 20);
  assert.equal(frame.color, '#abc', 'the --viz custom property, trimmed');
  assert.equal(frame.fill, '#abc');
  assert.equal(canvas.width, 200);
  assert.deepEqual(canvas.g.ops[0], ['clear', 0, 0, 200, 60]);

  canvas.cssViz = '';
  viz.draw();
  assert.equal(frame.color, '#6cf', 'the fallback colour');
  viz.color = 'red';
  viz.draw();
  assert.equal(frame.color, 'red');
  let n = 0;
  viz.color = () => `hsl(${++n}, 50%, 50%)`;
  viz.draw();
  viz.draw();
  assert.equal(frame.color, 'hsl(2, 50%, 50%)', 'a colour function is read every frame');
  assert.equal(new Visualizer(canvas, analyser, { sampleRate: 8000 }).sampleRate, 8000);
  assert.equal(new Visualizer(canvas, { frequencyBinCount: 1 }).sampleRate, 44100);
});

test('start runs a frame loop with capped frame times; stop and off end it', (t) => {
  const frames = fakeFrames(t);
  const dts = [];
  let resets = 0;
  const probe = { name: 'probe', create: () => ({ draw: ({ dt }) => dts.push(dt), reset: () => resets++ }) };
  const viz = new Visualizer(fakeCanvas(), fakeAnalyser(), { modes: [probe] });
  viz.start();
  viz.start(); // a second start is a no-op
  assert.equal(frames.pending(), 1);
  assert.ok(viz.running);
  frames.run(1000);
  frames.run(1016);
  frames.run(5000);
  assert.deepEqual(dts, [16.7, 16, 100]);
  viz.stop();
  assert.equal(frames.pending(), 0);
  assert.ok(!viz.running);
  assert.equal(resets, 1);
  viz.start();
  frames.run(9000);
  assert.equal(dts.at(-1), 16.7, 'the frame clock restarts after a stop');
  viz.setMode(OFF);
  assert.ok(!viz.running);
  viz.start();
  assert.equal(frames.pending(), 0, 'off never starts the loop');
  viz.draw(); // drawing while off only clears
});

test('bars and scope draw from the analyser', (t) => {
  fakeFrames(t);
  const canvas = fakeCanvas({ w: 120, h: 50 });
  const viz = new Visualizer(canvas, fakeAnalyser(), { modes: [bars({ bands: 6 }), scope()] });
  viz.draw();
  const fills = canvas.g.ops.filter((o) => o[0] === 'fill');
  assert.equal(fills.length, 6);
  assert.ok(fills.every(([, , y, , h]) => h > 0 && y + h === 100), 'bars stand on the bottom edge');
  for (let i = 0; i < 30; i++) viz.draw();
  const tall = canvas.g.ops.filter((o) => o[0] === 'fill').at(-1);
  assert.ok(Math.abs(tall[4] - 100) < 1, 'a full-scale signal fills the height');
  viz.stop(); // resets the bars
  canvas.g.ops.length = 0;
  viz.draw();
  assert.ok(canvas.g.ops.filter((o) => o[0] === 'fill')[0][4] < 100 * 0.7, 'after a stop the bars rise again from zero');

  viz.setMode('scope');
  canvas.g.ops.length = 0;
  viz.draw();
  const ops = canvas.g.ops.map((o) => o[0]);
  assert.deepEqual([ops[1], ops[2], ops.at(-1)], ['begin', 'move', 'stroke']);
  assert.equal(ops.filter((o) => o === 'line').length, 239);
  assert.equal(canvas.g.ops.at(-1)[2], 3, 'line width follows devicePixelRatio');
});
