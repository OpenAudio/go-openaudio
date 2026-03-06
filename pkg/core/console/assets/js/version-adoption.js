(function () {
  var loading = document.getElementById('version-adoption-loading');
  var content = document.getElementById('version-adoption-content');
  if (!loading || !content) return;

  fetch('/console/api/version-adoption')
    .then(function (r) { return r.json(); })
    .then(function (d) {
      loading.classList.add('hidden');
      content.classList.remove('hidden');

      var selfV = d.selfVersion || '—';
      var latestV = d.latestVersion || '—';
      var total = d.totalNodes || 0;
      var onLatest = d.onLatest || 0;
      var pct = d.percentOnLatest || 0;
      var segments = d.segments || [];
      var isSelfLatest = selfV === latestV;

      var html = '<div class="flex flex-wrap items-center gap-4">';
      html += '<div class="flex items-center gap-2 shrink-0">';
      html += '<span class="text-xs font-medium text-secondary uppercase tracking-wider">You</span>';
      html += '<span class="px-2 py-0.5 rounded text-sm font-mono bg-secondary/50 text-tertiary">' + escapeHtml(selfV) + '</span>';
      if (isSelfLatest) {
        html += '<span class="text-xs text-green-400">✓ latest</span>';
      } else if (latestV !== '—') {
        html += '<span class="text-xs text-amber-400">behind</span>';
      }
      html += '</div>';

      html += '<div class="flex-1 min-w-[120px] flex items-center gap-2">';
      html += '<div class="flex-1 h-3 rounded-full overflow-hidden bg-secondary flex" title="Version distribution">';
      segments.forEach(function (s) {
        var w = total > 0 ? Math.max(2, (s.count / total) * 100) : 0;
        var cls = s.latest ? 'bg-green-500' : 'bg-amber-900/40';
        if (s.version === 'unknown') cls = 'bg-secondary';
        html += '<div class="' + cls + ' transition-all shrink-0" style="flex:0 0 ' + w + '%" title="' + escapeHtml(s.version) + ': ' + s.count + '"></div>';
      });
      html += '</div>';
      html += '<span class="text-sm font-semibold text-tertiary shrink-0 w-14 text-right">' + pct + '%</span>';
      html += '</div>';

      html += '<div class="text-xs text-secondary shrink-0">';
      html += '<span class="text-tertiary font-medium">' + onLatest + '</span>/' + total + ' on latest';
      if (latestV !== '—') {
        html += ' <span class="text-secondary">(' + escapeHtml(latestV) + ')</span>';
      }
      html += '</div>';
      html += '</div>';

      content.innerHTML = html;
    })
    .catch(function (e) {
      loading.textContent = 'Version adoption unavailable';
      loading.classList.remove('hidden');
      content.classList.add('hidden');
    });

  function escapeHtml(s) {
    if (!s) return '';
    var d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  }
})();
