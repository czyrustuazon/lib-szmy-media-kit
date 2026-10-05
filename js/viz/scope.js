// Waveform scope: the time-domain signal, decimated to one point per pixel column while keeping
// each column's peak (so transients are not averaged away), boosted so quiet passages show.

// One y value (0..height) per column for width columns of 8-bit samples (128 = silence).
export function scopePoints(wave, width, height, gain = 1.6) {
  const per = Math.max(1, Math.floor(wave.length / width));
  const ys = new Array(width);
  for (let x = 0; x < width; x++) {
    let best = 128;
    for (let k = 0; k < per; k++) {
      const s = wave[Math.min(wave.length - 1, x * per + k)];
      if (Math.abs(s - 128) > Math.abs(best - 128)) best = s; // keep the peak
    }
    ys[x] = height / 2 + ((best - 128) / 128) * (height / 2) * gain;
  }
  return ys;
}

export function scope({ name = 'scope', gain = 1.6, lineWidth = 1.5 } = {}) {
  return {
    name,
    create({ analyser }) {
      const wave = new Uint8Array(analyser.fftSize);
      return {
        draw({ g, width, height, dpr }) {
          analyser.getByteTimeDomainData(wave);
          g.lineWidth = Math.max(2, dpr * lineWidth);
          g.beginPath();
          scopePoints(wave, width, height, gain).forEach((y, x) => (x === 0 ? g.moveTo(x, y) : g.lineTo(x, y)));
          g.stroke();
        },
      };
    },
  };
}
