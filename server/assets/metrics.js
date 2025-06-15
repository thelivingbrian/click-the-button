      // Hold onto the full dataset so we can slice it for each range.
      let fullLabels = [], fullClicks = [], fullViews = [];
      let chart;

      async function load() {
        const res  = await fetch('metrics');
        const data = await res.json();

        fullLabels = data.map(p => new Date(p.ts * 1000));
        fullClicks = data.map(p => p.clicks);
        fullViews  = data.map(p => p.views);

        // Default view is All‑Time.
        createChart(fullLabels, fullClicks, fullViews);
      }

      function createChart(labels, clicks, views) {
        const ctx = document.getElementById('mChart');
        if (chart) {
          chart.destroy();
        }
        chart = new Chart(ctx, {
          type: 'line',
          data: {
            labels,
            datasets: [
              { label: 'Clicks', data: clicks, borderWidth: 1 },
              { label: 'Views',  data: views,  borderWidth: 1 }
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
                  displayFormats: {
                    hour: 'MMM d, h:mm a'
                  }
                }
              },
              y: { beginAtZero: true }
            }
          }
        });
      }

      function filterRange(range) {
        if (range === 'all') {
          createChart(fullLabels, fullClicks, fullViews);
          return;
        }

        const now = Date.now();
        const ranges = {
          '1h': 1 * 60 * 60 * 1000,
          '1d': 24 * 60 * 60 * 1000,
          '2d': 2 * 24 * 60 * 60 * 1000,
          '1w': 7 * 24 * 60 * 60 * 1000,
        };
        const cutoff = now - ranges[range];

        const idx = fullLabels.findIndex(d => d.getTime() >= cutoff);
        // If nothing found (all data older), fall back to full set.
        const start = idx === -1 ? 0 : idx;

        createChart(
          fullLabels.slice(start),
          fullClicks.slice(start),
          fullViews.slice(start)
        );
      }

      // Attach handlers to buttons once DOM is loaded.
      document.addEventListener('DOMContentLoaded', () => {
        document.querySelectorAll('.range-buttons button').forEach(btn => {
          btn.addEventListener('click', () => {
            filterRange(btn.dataset.range);
          });
        });

        load();
      });