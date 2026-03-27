document.addEventListener('DOMContentLoaded', function () {
  // Top-level tab switching (Consensus / Validators / Eth)
  document.querySelectorAll('.node-tab-btn').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var tab = btn.dataset.tab;

      document.querySelectorAll('.node-tab-btn').forEach(function (b) {
        b.classList.remove('border-primary');
        b.classList.add('border-transparent');
      });
      btn.classList.remove('border-transparent');
      btn.classList.add('border-primary');

      document.querySelectorAll('.node-tab-panel').forEach(function (p) {
        p.classList.add('hidden');
      });
      var panel = document.getElementById('panel-' + tab);
      if (panel) panel.classList.remove('hidden');
    });
  });

  // List / Matrix sub-toggle within each panel
  document.querySelectorAll('.view-toggle-btn').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var panel = btn.dataset.panel;
      var view = btn.dataset.view;

      // Update sub-tab button styles within this panel
      var parentBar = btn.parentElement;
      parentBar.querySelectorAll('.view-toggle-btn').forEach(function (b) {
        b.classList.remove('border-b-2', 'border-primary');
      });
      btn.classList.add('border-b-2', 'border-primary');

      // Show/hide list vs matrix for this panel
      var listEl = document.getElementById(panel + '-list');
      var matrixEl = document.getElementById(panel + '-matrix');
      if (view === 'list') {
        if (listEl) listEl.classList.remove('hidden');
        if (matrixEl) matrixEl.classList.add('hidden');
      } else {
        if (listEl) listEl.classList.add('hidden');
        if (matrixEl) {
          matrixEl.classList.remove('hidden');
          loadMatrix(matrixEl);
        }
      }
    });
  });
});

// Lazy-load the matrix view into a given matrix container element.
// Each matrix container has a .matrix-loading and .matrix-content child.
// Once loaded, it caches the result via data-loaded attribute.
async function loadMatrix(container) {
  var loading = container.querySelector('.matrix-loading');
  var content = container.querySelector('.matrix-content');
  if (!loading || !content || content.dataset.loaded === '1') return;
  loading.classList.remove('hidden');
  content.classList.add('hidden');
  try {
    var resp = await fetch('/console/api/matrix');
    var data = await resp.json();
    if (!resp.ok) {
      loading.textContent = 'Error: ' + (data.error || resp.status);
      return;
    }
    var refList = data.referenceList || [];
    var rows = data.rows || [];
    if (refList.length === 0) {
      loading.textContent = 'No nodes in core_validators';
      return;
    }
    var html = '<p class="text-sm text-secondary mb-4">\u2713 = node includes this endpoint in core_validators, \u2717 = missing</p>';
    html += '<div class="matrix-table-wrapper"><table class="matrix-table text-sm"><thead><tr><th class="matrix-corner">Node</th>';
    refList.forEach(function (ep) {
      var short = ep.replace(/^https?:\/\//, '').replace(/\/$/, '');
      var label = short.length > 16 ? short.slice(0, 16) + '...' : short;
      var safeEp = ep.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
      html += '<th class="matrix-col-header"><span class="matrix-header-label">' + label + '</span><span class="matrix-tooltip">' + safeEp + '</span></th>';
    });
    html += '</tr></thead><tbody>';
    rows.forEach(function (r) {
      var nodeSet = new Set((r.endpoints || []).map(function (e) {
        return e.toLowerCase().replace(/\/$/, '');
      }));
      var name = r.base.replace(/^https?:\/\//, '').replace(/\/$/, '');
      var safeBase = r.base.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
      html += '<tr><td class="matrix-row-header font-mono sticky left-0">' + name + '<span class="matrix-tooltip">' + safeBase + '</span></td>';
      var rowBaseNorm = r.base.toLowerCase().replace(/\/$/, '');
      refList.forEach(function (refEp) {
        var n = refEp.toLowerCase().replace(/\/$/, '');
        var ok = !r.err && nodeSet.has(n);
        var cellClass = ok ? 'matrix-ok' : r.err ? 'matrix-err' : 'matrix-missing';
        if (rowBaseNorm === n) cellClass += ' matrix-diagonal';
        html += '<td class="' + cellClass + '">' + (r.err ? '?' : ok ? '\u2713' : '\u2717') + '</td>';
      });
      html += '</tr>';
    });
    html += '</tbody></table></div>';
    content.innerHTML = html;
    content.classList.remove('hidden');
    loading.classList.add('hidden');
    content.dataset.loaded = '1';
  } catch (e) {
    loading.textContent = 'Error: ' + e.message;
  }
}
