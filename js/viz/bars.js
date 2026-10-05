// Equalizer bars: log-spaced bands (80 Hz to 12 kHz by default). A bar rises quickly to a new
// peak and falls smoothly (see step), on a 0..255 scale.

// The analyser bin at each band edge: bands + 1 indexes, log-spaced from minHz to maxHz (capped
// at Nyquist).
export function bandEdges(sampleRate, bins, bands = 24, minHz = 80, maxHz = 12000) {
  const hi = Math.min(maxHz, sampleRate / 2);
  const edges = [];
  for (let i = 0; i <= bands; i++) {
    const f = minHz * Math.pow(hi / minHz, i / bands);
    edges.push(Math.min(bins - 1, Math.round((f / (sampleRate / 2)) * (bins - 1))));
  }
  return edges;
}

// Next bar value: rises quickly toward a louder target, falls smoothly (about
// 10% per 60 Hz frame) so frame-rate jitter and brief dips don't flutter.
export function step(v, target, dtMs) {
  if (target > v) return v + (target - v) * 0.6;
  const next = v * Math.pow(0.9, dtMs / 16.7);
  return next < 1 ? 0 : Math.max(target, next);
}

// Band levels from one frame of frequency data: the mean of each band's bins (at least two, so
// narrow low bands don't ride a single noisy bin), blended with the neighbours so adjacent bars
// move together, then square-root scaled (closer to how loudness is perceived). 0..255.
export function bandLevels(freq, edges) {
  const n = edges.length - 1;
  const raw = new Array(n);
  for (let i = 0; i < n; i++) {
    const a = edges[i];
    const b = Math.min(freq.length, Math.max(a + 2, edges[i + 1]));
    let sum = 0;
    for (let k = a; k < b; k++) sum += freq[k];
    raw[i] = sum / Math.max(1, b - a);
  }
  return raw.map((_, i) => {
    const l = raw[Math.max(0, i - 1)];
    const r = raw[Math.min(n - 1, i + 1)];
    return Math.sqrt((l * 0.25 + raw[i] * 0.5 + r * 0.25) / 255) * 255;
  });
}

export function bars({ name = 'bars', bands = 24, minHz = 80, maxHz = 12000 } = {}) {
  return {
    name,
    create({ analyser, sampleRate }) {
      const freq = new Uint8Array(analyser.frequencyBinCount);
      const edges = bandEdges(sampleRate, analyser.frequencyBinCount, bands, minHz, maxHz);
      let level = new Array(bands).fill(0);
      return {
        draw({ g, width, height, dt }) {
          analyser.getByteFrequencyData(freq);
          const gap = Math.max(2, width / bands / 6);
          const bw = (width - gap * (bands - 1)) / bands;
          bandLevels(freq, edges).forEach((v, i) => {
            level[i] = step(level[i], v, dt);
            const bh = (level[i] / 255) * height;
            g.fillRect(i * (bw + gap), height - bh, bw, bh);
          });
        },
        reset() {
          level = new Array(bands).fill(0);
        },
      };
    },
  };
}
