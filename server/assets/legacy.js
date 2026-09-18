// Frozen history only. This page never opens an EventSource or a live stream.
(async () => {
  const status = document.getElementById('chart-status');
  try {
    const response = await fetch('/legacy/history.json');
    if (!response.ok) throw new Error('History unavailable');
    const history = await response.json();
    const chart = new Chart(document.getElementById('mChart'), {
      type: 'line',
      data: { labels: history.map(p => new Date(p.ts * 1000)), datasets: [
        {label: '🐕 (Dog)', data: history.map(p => p.clicksA), borderWidth: 1},
        {label: '🐈 (Cat)', data: history.map(p => p.clicksB), borderWidth: 1}
      ]},
      options: {responsive:true, animation:false, scales:{x:{type:'time'},y:{beginAtZero:true}}}
    });
    status.textContent = `${history.length} preserved snapshots. This chart is frozen.`;
    const ranges = {'5m':300, '1h':3600, '1d':86400, '2d':172800, '1w':604800};
    document.querySelectorAll('[data-range]').forEach(button => button.addEventListener('click', () => {
      const x = chart.options.scales.x;
      if (button.dataset.range === 'all') {delete x.min; delete x.max;}
      else {x.max = history.at(-1).ts * 1000; x.min = x.max - ranges[button.dataset.range] * 1000;}
      chart.update();
    }));
  } catch (_) { status.textContent = 'The chart could not load. The preserved history is available below.'; }
})();
