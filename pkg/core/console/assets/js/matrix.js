document.addEventListener('DOMContentLoaded', function () {
  document.querySelectorAll('.tab-btn').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var tab = btn.dataset.tab;
      document.querySelectorAll('.tab-btn').forEach(function (b) {
        b.classList.remove('border-b-2', 'border-primary');
      });
      document.querySelectorAll('.tab-panel').forEach(function (p) {
        p.classList.add('hidden');
      });
      btn.classList.add('border-b-2', 'border-primary');
      document.getElementById('panel-' + tab).classList.remove('hidden');
      if (tab === 'matrix') loadMatrix();
    });
  });
});

async function loadMatrix() {
  var loading = document.getElementById('matrix-loading');
  var content = document.getElementById('matrix-content');
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
    var html = '<p class="text-sm text-secondary mb-4">✓ = node includes this endpoint in core_validators, ✗ = missing</p>';
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
        html += '<td class="' + cellClass + '">' + (r.err ? '?' : ok ? '✓' : '✗') + '</td>';
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
