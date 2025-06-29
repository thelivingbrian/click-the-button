let fullLabels = [], fullClicksA = [], fullClicksB = [];
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
  if (fullLabels.length == 0 && fullClicksA.length == 0 && fullClicksB.length == 0) {
      const res  = await fetch('metrics/history');
      const data = await res.json();
      if (!data) {
          return
      }
      
      fullLabels = data.map(p => new Date(p.ts * 1000));
      fullClicksA = data.map(p => p.clicksA);
      fullClicksB  = data.map(p => p.clicksB);
  }

  const es = getEventStream();
  es.addEventListener('point', e => {
    const p = JSON.parse(e.data);
    fullLabels.push(new Date(p.ts * 1000));
    fullClicksA.push(p.clicksA);
    fullClicksB.push(p.clicksB);

    if (chart) {
      updateWindow();             // slide window
      chart.update('none');
    }
  });
  es.onerror = () => console.log('SSE error – browser will retry automatically');

}

load() // Occurs on page load


/* ────────────────── Event stream ────────────────── */

let es;

function getEventStream() {
  if (es) return es;

  es = new EventSource('/metrics/feed');
  window.addEventListener('beforeunload', () => es.close());
  return es;
}


/* ────────────────── Chart ────────────────── */

function setupChart(){
  addButtonListeners()
  createChart()
}

function addButtonListeners(){
    document
    .querySelectorAll('.range-buttons button')
    .forEach(btn =>
      btn.addEventListener('click', () => setRange(btn.dataset.range))
    );
}

function createChart() {
  const ctx = document.getElementById('mChart');
  if (chart) chart.destroy();

  chart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: fullLabels,
      datasets: [
        { label: 'Clicks A', data: fullClicksA, borderWidth: 1 },
        { label: 'Clicks B',  data: fullClicksB,  borderWidth: 1 }
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