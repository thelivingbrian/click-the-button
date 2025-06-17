let fullLabels = [], fullClicks = [], fullViews = [];
let chart;
let currentRange = 'all';

const ranges = {                   // range is in ms
  '5m':  5 * 60 * 1000,
  '1h': 60 * 60 * 1000,
  '1d': 24 * 60 * 60 * 1000,
  '2d':  2 * 24 * 60 * 60 * 1000,
  '1w':  7 * 24 * 60 * 60 * 1000,
};

async function load() {
  const res  = await fetch('metrics');
  const data = await res.json();

  fullLabels = data.map(p => new Date(p.ts * 1000));
  fullClicks = data.map(p => p.clicks);
  fullViews  = data.map(p => p.views);

  createChart();                // build once
  updateWindow();               // needed ?

  const es = new EventSource('/metrics/feed');
  es.addEventListener('point', e => {
    const p = JSON.parse(e.data);
    fullLabels.push(new Date(p.ts * 1000));
    fullClicks.push(p.clicks);
    fullViews.push(p.views);

    updateWindow();             // slide window if not “all”
    chart.update('none');
  });
  es.onerror = () => console.log('SSE error – browser will retry automatically');
}

function createChart() {
  const ctx = document.getElementById('mChart');
  if (chart) chart.destroy();

  chart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: fullLabels,
      datasets: [
        { label: 'Clicks', data: fullClicks, borderWidth: 1 },
        { label: 'Views',  data: fullViews,  borderWidth: 1 }
      ]
    },
    options: {
      responsive: true,
      parsing: true,
      scales: {
        x: {
          type: 'time',
          time: {
            unit: 'hour',
            displayFormats: { hour: 'MMM d, h:mm a' }
          }
        },
        y: { beginAtZero: true }
      }
    }
  });
}

/* ───────────────── window helpers ───────────────── */

function setRange(range) {
  currentRange = range;
  updateWindow();
  chart.update('none');
}

function updateWindow() {
  const x = chart.options.scales.x;
  if (currentRange === 'all') {
    delete x.min;
    delete x.max;
    return;
  }
  const now = Date.now();
  x.max = now;
  x.min = now - ranges[currentRange];
}

document.addEventListener('DOMContentLoaded', () => {
  document
    .querySelectorAll('.range-buttons button')
    .forEach(btn =>
      btn.addEventListener('click', () => setRange(btn.dataset.range))
    );

  load();
});