(() => {
  const notice = document.getElementById('action-notice');
  const connection = document.getElementById('connection-status');
  let refreshing = false;
  async function refresh() {
    const url = document.body.dataset.liveUrl;
    if (!url || refreshing || document.hidden) return;
    refreshing = true;
    try {
      const response = await fetch(url, {cache: 'no-store'});
      if (response.status === 404) {
        document.querySelector('[data-round]').textContent = 'This event is no longer available.';
        delete document.body.dataset.liveUrl;
        connection.textContent = '';
        return;
      }
      if (!response.ok) throw new Error('Refresh failed');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      const incoming = doc.querySelector('[data-round]');
      const current = document.querySelector('[data-round]');
      if (!incoming || !current) throw new Error('Missing event');
      if (incoming.dataset.round !== current.dataset.round || incoming.dataset.status !== current.dataset.status) {
        current.replaceWith(incoming);
        notice.textContent = 'The event has closed. Results are archived and its discussion has been deleted.';
        if (incoming.dataset.status === 'archived') delete document.body.dataset.liveUrl;
      } else {
        doc.querySelectorAll('[data-refresh]').forEach(next => {
          const previous = document.getElementById(next.id);
          // Updating results must never replace a focused ballot control.
          if (previous && !previous.querySelector('[data-pending]') && !previous.contains(document.activeElement) && previous.outerHTML !== next.outerHTML) previous.replaceWith(next);
        });
      }
      connection.textContent = '';
    } catch (_) {
      connection.textContent = 'Live updates paused. Retrying… You can still submit a vote.';
    } finally { refreshing = false; }
  }
  document.addEventListener('submit', async event => {
    const confirmation = event.target.dataset.confirm;
    if (confirmation && !window.confirm(confirmation)) {
      event.preventDefault();
      return;
    }
    const form = event.target.closest('form[data-action]');
    if (!form) return;
    event.preventDefault();
    const button = event.submitter || form.querySelector('button');
    if (button.disabled) return;
    button.disabled = true;
    form.dataset.pending = "true";
    // Keep this identifier on network failures so retrying cannot double-count.
    if (!form.dataset.request) form.dataset.request = crypto.randomUUID();
    const url = new URL(form.action);
    url.searchParams.set('request', form.dataset.request);
    try {
      const response = await fetch(url, {method: 'POST', body: new URLSearchParams(new FormData(form)), headers: {'Accept': 'application/json'}});
      const result = await response.json();
      notice.textContent = result.message;
      if (response.ok) {
        delete form.dataset.request;
        if (form.hasAttribute('data-comment')) form.reset();
      }
      delete form.dataset.pending;
      await refresh();
    } catch (_) { notice.textContent = 'Could not confirm your submission. Please try again.'; }
    finally { delete form.dataset.pending; button.disabled = false; }
  });
  function configureFormat() {
    const kind = document.querySelector('input[name="kind"]:checked')?.value;
    if (!kind) return;
    const fixed = ['scale', 'stars', 'heat'].includes(kind);
    document.getElementById('custom-options').hidden = fixed;
    document.getElementById('fixed-options').hidden = !fixed;
    const options = document.getElementById('options');
    options.required = !fixed;
    document.getElementById('options-help').textContent = kind === 'pulse' ? 'Use one button label.' : kind === 'tug' ? 'Use exactly two options.' : 'Use 2–8 unique options.';
  }
  document.querySelectorAll('input[name="kind"]').forEach(input => input.addEventListener('change', configureFormat));
  configureFormat();
  setInterval(refresh, 3000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
})();
