// Canvas audio visualizer with pluggable modes.
//
// The core owns everything that is the same for every mode: the animation loop, sizing the
// canvas to its CSS box at the device pixel ratio, the colour, and switching modes. A mode is a
// plugin that only draws:
//
//   const myMode = {
//     name: 'dots',
//     // Called once, the first time the mode is shown. Returns the renderer; keep per-mode
//     // state (smoothing, buffers) in its closure.
//     create({ analyser, sampleRate }) {
//       const data = new Uint8Array(analyser.frequencyBinCount);
//       return {
//         draw({ g, width, height, dt, dpr, color }) { ... },  // every animation frame
//         reset() { ... },                                     // optional: the loop stopped
//       };
//     },
//   };
//
//   const viz = new Visualizer(canvas, analyser, { modes: [bars(), scope(), myMode] });
//   viz.setMode('dots');
//   viz.start();
//
// g is the canvas 2D context, already cleared, with fillStyle and strokeStyle set to color.
// width and height are in device pixels; dt is the milliseconds since the last frame (capped at
// 100, so a background tab coming back does not jump). "off" is always available.

export const OFF = 'off';

export class Visualizer {
  // color: a CSS colour, a function returning one (read every frame), or omitted to use the
  // canvas's --viz custom property (falling back to #6cf), so a theme switch is picked up live.
  constructor(canvas, analyser, { modes = [], mode, color, sampleRate } = {}) {
    this.canvas = canvas;
    this.analyser = analyser;
    this.sampleRate = sampleRate ?? analyser.context?.sampleRate ?? 44100;
    this.ctx2d = canvas.getContext('2d');
    this.color = color;
    this.plugins = new Map();
    this.renderers = new Map();
    this.raf = 0;
    this.last = 0;
    for (const p of modes) this.register(p);
    this.mode = mode ?? modes[0]?.name ?? OFF;
    if (this.mode !== OFF && !this.plugins.has(this.mode)) throw new RangeError(`unknown visualizer mode "${this.mode}"`);
  }

  // Adds a mode (or replaces one of the same name).
  register(plugin) {
    if (!plugin?.name || typeof plugin.create !== 'function') throw new TypeError('a visualizer mode needs a name and create()');
    if (plugin.name === OFF) throw new RangeError(`"${OFF}" is reserved`);
    this.plugins.set(plugin.name, plugin);
    this.renderers.delete(plugin.name);
    return this;
  }

  // The selectable mode names, "off" last.
  get modes() {
    return [...this.plugins.keys(), OFF];
  }

  setMode(mode) {
    if (mode !== OFF && !this.plugins.has(mode)) throw new RangeError(`unknown visualizer mode "${mode}"`);
    this.mode = mode;
    if (mode === OFF) this.stop();
  }

  get running() {
    return this.raf !== 0;
  }

  start() {
    if (this.raf || this.mode === OFF) return;
    const loop = (t) => {
      this.raf = requestAnimationFrame(loop);
      // Draw every frame: a fixed 33 ms throttle beats against 60 Hz vsync and judders.
      const dt = this.last ? Math.min(100, t - this.last) : 16.7;
      this.last = t;
      this.draw(dt);
    };
    this.raf = requestAnimationFrame(loop);
  }

  stop() {
    cancelAnimationFrame(this.raf);
    this.raf = 0;
    this.last = 0;
    for (const r of this.renderers.values()) r.reset?.();
    this.ctx2d.clearRect(0, 0, this.canvas.width, this.canvas.height);
  }

  // Draws one frame of the current mode (start() calls this; call it yourself to drive the
  // visualizer from your own loop).
  draw(dt = 16.7) {
    const { width, height, dpr } = this._size();
    const g = this.ctx2d;
    g.clearRect(0, 0, width, height);
    if (this.mode === OFF) return;
    const color = this._color();
    g.fillStyle = color;
    g.strokeStyle = color;
    this._renderer(this.mode).draw({ g, width, height, dt, dpr, color, analyser: this.analyser });
  }

  _renderer(name) {
    let r = this.renderers.get(name);
    if (!r) {
      r = this.plugins.get(name).create({ analyser: this.analyser, sampleRate: this.sampleRate });
      this.renderers.set(name, r);
    }
    return r;
  }

  _color() {
    if (typeof this.color === 'function') return this.color();
    if (this.color) return this.color;
    return getComputedStyle(this.canvas).getPropertyValue('--viz').trim() || '#6cf';
  }

  _size() {
    const dpr = globalThis.devicePixelRatio || 1;
    const width = Math.round(this.canvas.clientWidth * dpr);
    const height = Math.round(this.canvas.clientHeight * dpr);
    if (this.canvas.width !== width || this.canvas.height !== height) {
      this.canvas.width = width;
      this.canvas.height = height;
    }
    return { width, height, dpr };
  }
}
