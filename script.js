(function () {
    var saved = localStorage.getItem('ci-theme') || 'dark';
    if (saved === 'light') document.body.classList.add('light');
  
    var btn      = document.getElementById('themeToggle');
    var iconSun  = document.getElementById('iconSun');
    var iconMoon = document.getElementById('iconMoon');
  
    function syncIcons() {
      var isLight = document.body.classList.contains('light');
      iconSun.style.display  = isLight ? 'none' : '';
      iconMoon.style.display = isLight ? ''     : 'none';
    }
    syncIcons();
  
    if (btn) {
      btn.addEventListener('click', function () {
        document.body.classList.toggle('light');
        localStorage.setItem('ci-theme', document.body.classList.contains('light') ? 'light' : 'dark');
        syncIcons();
      });
    }
  })();
  
  // Helpers
  function fmtDur(secs) {
    if (!secs) return '—';
    if (secs < 60) return Math.round(secs) + 's';
    var h = Math.floor(secs / 3600);
    var m = Math.floor((secs % 3600) / 60);
    var s = Math.round(secs % 60);
    if (h > 0) return m > 0 ? h + 'h ' + m + 'm' : h + 'h';
    return s > 0 ? m + 'm ' + s + 's' : m + 'm';
  }
  
  function fmtDate(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    return d.toLocaleDateString('en-US', { day: '2-digit', month: 'short', year: 'numeric' })
      + ' ' + d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' });
  }
  
  function conclusionHtml(c) {
    if (!c) return '<span class="conclusion unknown">—</span>';
    var cls = ['success', 'failure', 'skipped', 'action_required'].includes(c) ? c : 'unknown';
    return '<span class="conclusion ' + cls + '">' + c.replace(/_/g, ' ') + '</span>';
  }
  
  function rateColorClass(r) { return r >= 80 ? 'green' : r >= 50 ? 'orange' : 'red'; }
  function rateBarColor(r)   { return r >= 80 ? 'var(--green)' : r >= 50 ? 'var(--orange)' : 'var(--red)'; }
  function healthColor(h)    { return h >= 80 ? 'var(--green)' : h >= 50 ? 'var(--orange)' : 'var(--red)'; }
  
  function esc(s) {
    return String(s || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }
  
  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }
  
  function hexToRgba(hex, alpha) {
    if (!hex || !hex.startsWith('#')) return 'rgba(88,166,255,' + alpha + ')';
    hex = hex.replace('#', '');
    if (hex.length === 3) hex = hex.split('').map(function (c) { return c + c; }).join('');
    var r = parseInt(hex.slice(0, 2), 16);
    var g = parseInt(hex.slice(2, 4), 16);
    var b = parseInt(hex.slice(4, 6), 16);
    return 'rgba(' + r + ',' + g + ',' + b + ',' + alpha + ')';
  }
  
  function colorWithAlpha(color, alpha) {
    if (!color) return 'rgba(88,166,255,' + alpha + ')';
    if (color.startsWith('#')) return hexToRgba(color, alpha);
    if (color.startsWith('hsl(')) return color.replace('hsl(', 'hsla(').replace(')', ', ' + alpha + ')');
    return color;
  }
  
  function getChartPalette(count) {
    var root = getComputedStyle(document.body);
    var cssColors = [
      root.getPropertyValue('--chart-1').trim(),
      root.getPropertyValue('--chart-2').trim(),
      root.getPropertyValue('--chart-3').trim(),
      root.getPropertyValue('--chart-4').trim(),
      root.getPropertyValue('--chart-5').trim(),
      root.getPropertyValue('--chart-6').trim(),
      root.getPropertyValue('--chart-7').trim(),
      root.getPropertyValue('--chart-8').trim()
    ].filter(Boolean);
  
    var colors = cssColors.slice();
    while (colors.length < count) {
      colors.push('hsl(' + Math.round((colors.length * 137.5) % 360) + ', 62%, 56%)');
    }
    return colors.slice(0, count);
  }
  
  // Structured log renderer
  
  var LOG_CATEGORIES = {
    'crash / timeout':     { cls: 'cat-crash',   icon: '!', label: 'Crash / Timeout' },
    'network failure':     { cls: 'cat-network',  icon: '!', label: 'Network Failure' },
    'test failure':        { cls: 'cat-test',     icon: '!', label: 'Test Failure' },
    'build failure':       { cls: 'cat-build',    icon: '!', label: 'Build Failure' },
    'fatal runtime error': { cls: 'cat-fatal',    icon: '!', label: 'Fatal Runtime Error' },
  };
  
  function categoryMeta(name) {
    if (!name) return { cls: 'cat-default', icon: '·', label: 'Signal' };
    return LOG_CATEGORIES[name.toLowerCase()] || { cls: 'cat-default', icon: '·', label: name };
  }
  
  var LOG_KEYWORDS = [
    { re: /\b(panic|fatal|segmentation violation|deadlock|timed out|FAIL)\b/gi, cls: 'log-kw-fatal' },
    { re: /\b(error|##\[error\]|compilation terminated|exit status \d+)\b/gi,   cls: 'log-kw-error' },
    { re: /\b(warning|warn)\b/gi,                                                cls: 'log-kw-warn'  },
    { re: /\b(ok|pass|passed|success)\b/gi,                                      cls: 'log-kw-pass'  },
    { re: /(\/[^\s:,)]+\.[a-z]{1,6}(:\d+)?)/g,                                  cls: 'log-kw-path'  },
    { re: /(`[^`]+`|\[[^\]]+\])/g,                                               cls: 'log-kw-code'  },
  ];
  
  function highlightLine(text) {
    var out = '';
    var pos = 0;
    var safety = 0;
  
    while (pos < text.length && safety++ < 500) {
      var best = null;
      LOG_KEYWORDS.forEach(function (kw) {
        kw.re.lastIndex = pos;
        var m = kw.re.exec(text);
        if (m && m.index >= pos) {
          if (!best || m.index < best.index || (m.index === best.index && m[0].length > best.length)) {
            best = { index: m.index, length: m[0].length, cls: kw.cls, raw: m[0] };
          }
        }
        kw.re.lastIndex = 0;
      });
  
      if (!best) { out += esc(text.slice(pos)); break; }
      if (best.index > pos) out += esc(text.slice(pos, best.index));
      out += '<span class="' + best.cls + '">' + esc(best.raw) + '</span>';
      pos = best.index + best.length;
    }
    return out;
  }
  
  function parseLogSnippet(snippet) {
    if (!snippet || snippet.trim() === '(no actionable failure signal found in log)') return null;
  
    var lines = snippet.split('\n');
    var topCategory = '';
    var signalCount = 0;
    var sections    = [];
    var currentSection = null;
  
    lines.forEach(function (line) {
      var headerMatch = line.match(/^\[(.+?)\]\s+[—-]+\s+(\d+)\s+signal/i);
      if (headerMatch) {
        topCategory = headerMatch[1];
        signalCount = parseInt(headerMatch[2], 10);
        return;
      }
      if (/^[─\-]{10,}/.test(line.trim())) return;
  
      var catMatch = line.match(/^->\s+(.+)/);
      if (catMatch) {
        currentSection = { category: catMatch[1].trim(), lines: [] };
        sections.push(currentSection);
        return;
      }
      if (currentSection && line.trim()) currentSection.lines.push(line.trim());
    });
  
    return { topCategory: topCategory, signalCount: signalCount, sections: sections };
  }
  
  function renderStructuredLog(snippet) {
    var parsed = parseLogSnippet(snippet);
    var html   = '<div class="log-body">';
  
    if (!parsed || parsed.sections.length === 0) {
      html += '<div class="log-no-signal">'
        + esc(snippet || '(no actionable failure signal found in log)')
        + '</div>';
    } else {
      var topMeta = categoryMeta(parsed.topCategory);
      html += '<div class="log-cat-header">'
        + '<span>Top issue:</span>'
        + '<span class="log-cat-badge ' + topMeta.cls + '">' + esc(topMeta.icon) + ' ' + esc(topMeta.label) + '</span>'
        + '<span class="log-cat-count">' + parsed.signalCount + ' signal' + (parsed.signalCount !== 1 ? 's' : '') + '</span>'
        + '</div>';
  
      parsed.sections.forEach(function (sec) {
        var meta = categoryMeta(sec.category);
        html += '<div class="log-cat-section">';
  
        if (parsed.sections.length > 1) {
          html += '<div style="padding:5px 14px 2px;font-family:\'JetBrains Mono\',monospace;font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:.5px">'
            + '<span class="log-cat-badge ' + meta.cls + '" style="font-size:10px;padding:1px 6px">' + esc(meta.icon) + ' ' + esc(meta.label) + '</span>'
            + '</div>';
        }
  
        html += '<div class="log-signals">';
        sec.lines.forEach(function (line) {
          html += '<div class="log-signal-line">'
            + '<span class="log-signal-arrow">›</span>'
            + '<span class="log-signal-text">' + highlightLine(line) + '</span>'
            + '</div>';
        });
        html += '</div></div>';
      });
    }
  
    return html + '</div>';
  }
  
  // State
  var allWorkflows = [];
  var activeFilter = 'all';
  var searchQuery  = '';
  var chartsBuilt  = false;
  
  // Global charts
  function buildCharts(workflows) {
    if (chartsBuilt) return;
    chartsBuilt = true;
  
    var active       = workflows.filter(function (w) { return w.total_runs > 0; });
    var labels       = active.map(function (w) { return w.name; });
    var successRates = active.map(function (w) { return parseFloat((100 - w.failure_rate).toFixed(1)); });
    var palette      = getChartPalette(labels.length);
  
    function gridColor()  { return cssVar('--border') || '#30363d'; }
    function mutedColor() { return cssVar('--muted')  || '#8b949e'; }
  
    Chart.defaults.global.defaultFontFamily = "'JetBrains Mono', monospace";
    Chart.defaults.global.defaultFontColor  = mutedColor();
    Chart.defaults.global.defaultFontSize   = 11;
  
    // Pie: passing vs failing workflows
    var pieColors = active.map(function (w) {
      return w.failure_rate > 20
        ? hexToRgba(cssVar('--red'),   0.85)
        : hexToRgba(cssVar('--green'), 0.85);
    });
  
    new Chart(document.getElementById('chartPie'), {
      type: 'pie',
      data: {
        labels: labels,
        datasets: [{ data: active.map(function (w) { return w.total_runs; }), backgroundColor: pieColors, borderWidth: 0 }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        legend: {
          position: 'right',
          labels: {
            boxWidth: 12,
            fontSize: 11,
            fontColor: mutedColor(),
          }
        },
        tooltips: {
          callbacks: {
            label: function (item, data) {
              var w = active[item.index];
              return ' ' + data.labels[item.index]
                + ': ' + (w.failure_rate > 20 ? 'failing' : 'passing')
                + ' · ' + (100 - w.failure_rate).toFixed(1) + '% success'
                + ' · ' + w.total_runs + ' runs';
            }
          }
        }
      }
    });
  
    // Line: execution time per workflow, last 7 runs
    var lineDatasets = active.map(function (w, wi) {
      var recent = (w.recent_runs || []).slice(0, 7).reverse();
      var color  = palette[wi % palette.length];
      return {
        label: w.name,
        data: recent.map(function (r) {
          var started = r.run_started_at || r.created_at;
          return parseFloat(((new Date(r.updated_at) - new Date(started)) / 60000).toFixed(2));
        }),
        borderColor:          color,
        backgroundColor:      'transparent',
        borderWidth:          2,
        pointRadius:          3,
        pointBackgroundColor: color,
        fill:                 false,
        lineTension:          0.3
      };
    });
  
    var maxSlots = active.reduce(function (m, w) {
      return Math.max(m, Math.min((w.recent_runs || []).length, 7));
    }, 0);
    var slotLabels = [];
    for (var i = maxSlots; i >= 1; i--) slotLabels.push(i === 1 ? 'latest' : i);
  
    new Chart(document.getElementById('chartBar'), {
      type: 'line',
      data: { labels: slotLabels, datasets: lineDatasets },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        legend: { labels: { boxWidth: 12, fontSize: 11, fontColor: mutedColor() } },
        scales: {
          xAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { fontColor: mutedColor(), fontSize: 11 } }],
          yAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { beginAtZero: true, fontColor: mutedColor(), fontSize: 11, callback: function (v) { return v + 'm'; } } }]
        },
        tooltips: {
          mode: 'index',
          intersect: false,
          callbacks: {
            label: function (item, data) {
              if (!item.yLabel) return null;
              return ' ' + data.datasets[item.datasetIndex].label + ': ' + item.yLabel + 'm';
            }
          }
        }
      }
    });
  
    // Doughnut: avg duration per workflow
    var durationLabels = [];
    var durationValues = [];
    active.forEach(function (w) {
      if (w.avg_duration_secs > 0) {
        durationLabels.push(w.name);
        durationValues.push(parseFloat((w.avg_duration_secs / 60).toFixed(2)));
      }
    });
  
    new Chart(document.getElementById('chartDuration'), {
      type: 'doughnut',
      data: {
        labels: durationLabels,
        datasets: [{ data: durationValues, backgroundColor: getChartPalette(durationLabels.length), borderWidth: 0 }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        legend: { position: 'right', labels: { boxWidth: 12, fontSize: 11, fontColor: mutedColor() } },
        tooltips: {
          callbacks: {
            label: function (item, data) {
              return ' ' + data.labels[item.index] + ': ' + data.datasets[0].data[item.index] + ' min';
            }
          }
        }
      }
    });
  
    // Bar: success rate per workflow
    new Chart(document.getElementById('chartSuccess'), {
      type: 'bar',
      data: {
        labels: labels,
        datasets: [{
          label: 'Success Rate %',
          data: successRates,
          backgroundColor: successRates.map(function (r) {
            return r >= 80 ? hexToRgba(cssVar('--green'), 0.75)
                 : r >= 50 ? hexToRgba(cssVar('--orange'), 0.75)
                           : hexToRgba(cssVar('--red'), 0.75);
          })
        }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        legend: { display: false },
        scales: {
          xAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { fontColor: mutedColor(), fontSize: 11 } }],
          yAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { beginAtZero: true, max: 100, fontColor: mutedColor(), fontSize: 11, callback: function (v) { return v + '%'; } } }]
        },
        tooltips: { callbacks: { label: function (item) { return ' ' + item.yLabel + '%'; } } }
      }
    });
  }
  
  // Filter helpers
  function hasRecentRun(wf) {
    if (!wf.last_run) return false;
    return (Date.now() - new Date(wf.last_run.run_started_at || wf.last_run.created_at).getTime()) < 7 * 24 * 60 * 60 * 1000;
  }
  
  function isRecentFailure(wf) {
    return (wf.recent_runs || []).some(function (run) {
      if (run.conclusion !== 'failure') return false;
      return (Date.now() - new Date(run.run_started_at || run.created_at).getTime()) < 7 * 24 * 60 * 60 * 1000;
    });
  }
  
  function matchesFilter(wf) {
    switch (activeFilter) {
      case 'failing':        return wf.failure_rate > 20;
      case 'passing':        return wf.failure_rate <= 20 && wf.total_runs > 0;
      case 'critical':       return wf.critical === true;
      case 'recent-run':     return hasRecentRun(wf);
      case 'recent-failure': return isRecentFailure(wf);
      case 'no-runs':        return wf.total_runs === 0;
      default:               return true;
    }
  }
  function ranWithin24h(wf) {
    if (!wf.last_run) return false;
    var t = new Date(wf.last_run.run_started_at || wf.last_run.created_at).getTime();
    return (Date.now() - t) < 24 * 60 * 60 * 1000;
  }
  
  function matchesSearch(wf) {
    if (!searchQuery) return true;
    var q = searchQuery.toLowerCase();
    return wf.name.toLowerCase().includes(q) || (wf.description || '').toLowerCase().includes(q);
  }
  
  function sortList(list) {
    switch (activeFilter) {
      case 'recent-run':
      case 'recent-failure':
        return list.slice().sort(function (a, b) {
          if (!a.last_run && !b.last_run) return 0;
          if (!a.last_run) return 1;
          if (!b.last_run) return -1;
          return new Date(b.last_run.created_at) - new Date(a.last_run.created_at);
        });
      case 'failing':
        return list.slice().sort(function (a, b) { return b.failure_rate - a.failure_rate; });
      case 'passing':
        return list.slice().sort(function (a, b) { return a.failure_rate - b.failure_rate; });
      default:
        return list.slice().sort(function (a, b) { return a.name.localeCompare(b.name); });
    }
  }
  
  function applyFilters() {
    var filtered = allWorkflows.filter(function (wf) { return matchesFilter(wf) && matchesSearch(wf); });
    var sorted   = sortList(filtered);
    renderTable(sorted);
    document.getElementById('filterCount').textContent = sorted.length + ' of ' + allWorkflows.length + ' workflows';
  }
  
  // Table renderer
  function renderTable(list) {
    var tbody = document.getElementById('wfBody');
    tbody.innerHTML = '';
    if (list.length === 0) {
      tbody.innerHTML = '<tr class="empty-row"><td colspan="6">No workflows match this filter.</td></tr>';
      return;
    }
    list.forEach(function (wf, idx) {
      var safeId   = wf.name.replace(/\W+/g, '-');
      var drawerId = 'drawer-' + safeId;
      var tr       = document.createElement('tr');
      tr.className = 'wf-row';
      tr.style.animationDelay = (idx * 30) + 'ms';
  
      var lr          = wf.last_run;
      var rate        = wf.failure_rate != null ? (100 - wf.failure_rate) : 0;
      var rateDisplay = (rate % 1 === 0) ? rate : rate.toFixed(1);
      var dotsHtml    = (wf.weather_history || []).map(function (h) { return '<div class="dot ' + h + '" title="' + h + '"></div>'; }).join('');
  
      tr.innerHTML =
        '<td>'
          + '<div class="wf-name-wrap">'
          + '<svg class="chevron" viewBox="0 0 16 16" fill="currentColor"><path d="M6.22 3.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.749.749 0 0 1-1.06-1.06L10 8 6.22 4.22a.75.75 0 0 1 0-1Z"/></svg>'
          + '<span class="wf-name">' 
            + esc(wf.name) 
            + (wf.critical ? '<span class="badge badge-critical">critical</span>' : '') 
            + (ranWithin24h(wf) ? '<span class="badge badge-recent">recent</span>' : '') 
            + '</span>'
          + '</div>'
          + '<div class="wf-desc">' + esc(wf.description || '') + '</div>'
        + '</td>'
        + '<td>'
          + (lr ? conclusionHtml(lr.conclusion) + '<div class="run-date">#' + lr.run_number + ' · ' + fmtDate(lr.run_started_at || lr.created_at) + '</div>'
                 : '<span class="conclusion unknown">no runs</span>')
        + '</td>'
        + '<td><div class="rate-wrap">'
          + '<div class="rate-bar-track"><div class="rate-bar-fill" style="width:' + Math.min(rate, 100) + '%;background:' + rateBarColor(rate) + '"></div></div>'
          + '<span class="rate-num ' + rateColorClass(rate) + '">' + rateDisplay + '%</span>'
        + '</div></td>'
        + '<td><div class="history-dots">' + (dotsHtml || '<span class="no-link">—</span>') + '</div></td>'
        + '<td><span class="dur">' + fmtDur(wf.avg_duration_secs) + '</span></td>'
        + '<td>'
          + (lr ? '<a class="run-link" href="' + esc(lr.html_url) + '" target="_blank" onclick="event.stopPropagation()">↗ View</a>'
                 : '<span class="no-link">—</span>')
        + '</td>';
  
      tbody.appendChild(tr);
      var drawerTr  = document.createElement('tr');
      drawerTr.className     = 'drawer-row';
      drawerTr.style.display = 'none';
      var drawerTd  = document.createElement('td');
      drawerTd.colSpan = 6;
      var drawerDiv = document.createElement('div');
      drawerDiv.className = 'drawer';
      drawerDiv.id        = drawerId;
      drawerTd.appendChild(drawerDiv);
      drawerTr.appendChild(drawerTd);
      tbody.appendChild(drawerTr);
  
      tr.addEventListener('click', function () {
        var open     = tr.classList.toggle('open');
        var drawerEl = document.getElementById(drawerId);
        drawerTr.style.display = open ? '' : 'none';
        if (open) {
          drawerEl.classList.add('open');
          if (!drawerEl.dataset.rendered) {
            drawerEl.dataset.rendered = '1';
            renderDrawer(wf, drawerEl);
          }
        } else {
          drawerEl.classList.remove('open');
        }
      });
    });
  }
  
  // Chip and search listeners
  document.querySelectorAll('.chip').forEach(function (chip) {
    chip.addEventListener('click', function () {
      document.querySelectorAll('.chip').forEach(function (c) { c.classList.remove('active'); });
      chip.classList.add('active');
      activeFilter = chip.dataset.filter;
  
      var chartsPanel = document.getElementById('chartsPanel');
      var tableWrap   = document.getElementById('tableWrap');
  
      if (activeFilter === 'chart') {
        chartsPanel.classList.add('open');
        tableWrap.style.display = 'none';
        if (allWorkflows.length > 0) buildCharts(allWorkflows);
        document.querySelectorAll('.chart-card').forEach(function (card, i) {
          card.style.animation = 'none';
          void card.offsetHeight;
          card.style.animation = '';
          card.style.animationDelay = (i * 80) + 'ms';
        });
      } else {
        chartsPanel.classList.remove('open');
        tableWrap.style.display = '';
        applyFilters();
      }
    });
  });
  
  document.querySelectorAll('.clickable').forEach(function (card) {
    card.addEventListener('click', function () {
      var filter = card.dataset.filter;
      if (!filter) return;
      document.querySelectorAll('.chip').forEach(function (c) { c.classList.remove('active'); });
      var target = document.querySelector('.chip[data-filter="' + filter + '"]');
      if (target) target.classList.add('active');
      document.getElementById('chartsPanel').classList.remove('open');
      document.getElementById('tableWrap').style.display = '';
      activeFilter = filter;
      applyFilters();
      document.querySelector('.filter-bar').scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  });
  
  document.getElementById('searchInput').addEventListener('input', function (e) {
    searchQuery = e.target.value.trim();
    applyFilters();
  });
  
  // Drawer renderer
  function renderDrawer(wf, drawerEl) {
    var runs = wf.recent_runs || [];
  
    if (runs.length === 0) {
      drawerEl.innerHTML = '<p class="no-runs-msg">No runs stored yet.</p>';
      return;
    }
  
    var safeWfName = wf.name.replace(/\W+/g, '-');
  
    drawerEl.innerHTML =
      '<div class="drawer-tabs">'
      + '<button class="drawer-tab active" data-tab="runs">Runs Table</button>'
      + '<button class="drawer-tab" data-tab="charts">Charts</button>'
      + '</div>'
      + '<div class="drawer-panel" id="panel-runs-'   + safeWfName + '"></div>'
      + '<div class="drawer-panel" id="panel-charts-' + safeWfName + '" style="display:none"></div>';
  
    // Runs table panel
    var runsPanel = document.getElementById('panel-runs-' + safeWfName);
    var html = '<div class="runs-title">Past Runs (' + runs.length + ')</div>'
      + '<table class="runs-table"><thead><tr>'
      + '<th>#</th><th>Conclusion</th><th>Started</th><th>Duration</th><th>Link</th><th>Failure Logs</th>'
      + '</tr></thead><tbody>';
  
    runs.forEach(function (run, ri) {
      var started    = run.run_started_at || run.created_at;
      var dur        = (new Date(run.updated_at) - new Date(started)) / 1000;
      var logPanelId = 'logpanel-' + safeWfName + '-' + ri;
      var hasFailed  = run.conclusion === 'failure' && run.failed_jobs && run.failed_jobs.length > 0;
  
      html += '<tr>'
        + '<td style="color:var(--muted);font-family:\'JetBrains Mono\',monospace">#' + run.run_number
        + (run.run_attempt > 1 ? ' <span style="font-size:10px;background:rgba(88,166,255,.15);color:var(--blue);border:1px solid rgba(88,166,255,.3);border-radius:3px;padding:1px 4px">attempt ' + run.run_attempt + '</span>' : '')
        + '</td>'
        + '<td>' + conclusionHtml(run.conclusion) + '</td>'
        + '<td style="color:var(--muted);font-family:\'JetBrains Mono\',monospace;font-size:12px">' + fmtDate(started) + '</td>'
        + '<td><span class="dur">' + fmtDur(dur) + '</span></td>'
        + '<td><a class="run-link" href="' + esc(run.html_url) + '" target="_blank">↗ View</a></td>'
        + '<td>' + (hasFailed ? '<button class="log-toggle" onclick="toggleLog(\'' + logPanelId + '\',this)">▶ Show logs</button>' : '<span class="no-link">—</span>') + '</td>'
        + '</tr>';
  
      if (hasFailed) {
        html += '<tr><td colspan="6" style="padding:0 12px 0"><div class="log-panel" id="' + logPanelId + '">';
        run.failed_jobs.forEach(function (job) {
          html += '<div class="log-job-block">'
            + '<div class="log-job-header">'
            + '<span class="log-job-name">✗ ' + esc(job.name) + '</span>'
            + '<a class="log-job-link" href="' + esc(job.html_url) + '" target="_blank">Open in GitHub ↗</a>'
            + '</div>'
            + renderStructuredLog(job.log_snippet)
            + '</div>';
        });
        html += '</div></td></tr>';
      }
    });
  
    html += '</tbody></table>';
    runsPanel.innerHTML = html;
  
    // Charts panel
    var chartsPanel = document.getElementById('panel-charts-' + safeWfName);
    var groupedId   = 'chart-grouped-'  + safeWfName;
    var passfailId  = 'chart-passfail-' + safeWfName;
    var lineId      = 'chart-line-'     + safeWfName;
  
    chartsPanel.innerHTML =
      '<div class="wf-chart-block chart-grouped">'
      + '<h3>Avg Job Duration by Variant</h3>'
      + '<p class="wf-chart-meta">Grouped by job · each bar = matrix variant · averaged across ' + runs.length + ' run(s) · seconds</p>'
      + '<canvas id="' + groupedId + '"></canvas>'
      + '</div>'
      + '<div class="wf-chart-block">'
      + '<h3>Pass / Fail per Run</h3>'
      + '<p class="wf-chart-meta">Color = outcome · height = total duration · oldest → newest</p>'
      + '<canvas id="' + passfailId + '"></canvas>'
      + '</div>'
      + '<div class="wf-chart-block">'
      + '<h3>Duration Trend</h3>'
      + '<p class="wf-chart-meta">Total run duration over time · minutes</p>'
      + '<canvas id="' + lineId + '"></canvas>'
      + '</div>';
  
    setTimeout(function () {
      var sorted = runs.slice().sort(function (a, b) { return new Date(a.created_at) - new Date(b.created_at); });
  
      function gridColor()  { return cssVar('--border') || '#30363d'; }
      function mutedColor() { return cssVar('--muted')  || '#8b949e'; }
  
      Chart.defaults.global.defaultFontFamily = "'JetBrains Mono', monospace";
      Chart.defaults.global.defaultFontColor  = mutedColor();
      Chart.defaults.global.defaultFontSize   = 11;
  
      // Grouped bar: avg duration per job and variant
      var jobGroups   = {};
      var allVariants = [];
  
      runs.forEach(function (run) {
        (run.jobs || []).forEach(function (job) {
          var parts   = job.name.split(' / ');
          var group   = parts[0].trim();
          var variant = parts.length > 1 ? parts.slice(1).join(' / ').trim() : job.name;
          if (!jobGroups[group]) jobGroups[group] = {};
          if (!jobGroups[group][variant]) jobGroups[group][variant] = [];
          jobGroups[group][variant].push(job.duration_sec);
          if (allVariants.indexOf(variant) === -1) allVariants.push(variant);
        });
      });
  
      var jobGroupNames  = Object.keys(jobGroups);
      var variantPalette = getChartPalette(allVariants.length);
  
      var groupedDatasets = allVariants.map(function (variant, vi) {
        return {
          label: variant,
          backgroundColor: colorWithAlpha(variantPalette[vi], 0.8),
          data: jobGroupNames.map(function (group) {
            var vals = (jobGroups[group] && jobGroups[group][variant]) ? jobGroups[group][variant] : [];
            if (!vals.length) return 0;
            return parseFloat((vals.reduce(function (a, b) { return a + b; }, 0) / vals.length).toFixed(1));
          }),
          _counts: jobGroupNames.map(function (group) {
            return ((jobGroups[group] && jobGroups[group][variant]) ? jobGroups[group][variant] : []).length;
          })
        };
      });
  
      if (jobGroupNames.length > 0) {
        new Chart(document.getElementById(groupedId), {
          type: 'bar',
          data: { labels: jobGroupNames, datasets: groupedDatasets },
          options: {
            responsive: true,
            maintainAspectRatio: true,
            legend: { position: 'bottom', labels: { boxWidth: 12, fontSize: 11, fontColor: mutedColor() } },
            scales: {
              xAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { fontColor: mutedColor(), fontSize: 11 } }],
              yAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { beginAtZero: true, fontColor: mutedColor(), fontSize: 11, callback: function (v) { return v + 's'; } } }]
            },
            tooltips: {
              callbacks: {
                label: function (item, data) {
                  var count = data.datasets[item.datasetIndex]._counts[item.index];
                  return ' ' + data.datasets[item.datasetIndex].label + ': ' + item.yLabel + 's (avg of ' + count + ' run' + (count === 1 ? '' : 's') + ')';
                }
              }
            }
          }
        });
      } else {
        document.getElementById(groupedId).parentElement.innerHTML +=
          '<p class="wf-chart-meta" style="text-align:center;padding:24px 0">No job data available.</p>';
      }
  
      // Pass / fail bar per run
      var runLabels    = sorted.map(function (r) { return '#' + r.run_number; });
      var runDurations = sorted.map(function (r) {
        var started = r.run_started_at || r.created_at;
        return parseFloat(((new Date(r.updated_at) - new Date(started)) / 60000).toFixed(2));
      });
      var runColors = sorted.map(function (r) {
        if (r.conclusion === 'success') return 'rgba(63,185,80,0.75)';
        if (r.conclusion === 'failure') return 'rgba(248,81,73,0.75)';
        return 'rgba(139,148,158,0.5)';
      });
  
      new Chart(document.getElementById(passfailId), {
        type: 'bar',
        data: { labels: runLabels, datasets: [{ label: 'Duration (min)', data: runDurations, backgroundColor: runColors }] },
        options: {
          responsive: true,
          maintainAspectRatio: true,
          legend: { display: false },
          scales: {
            xAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { fontColor: mutedColor(), fontSize: 11 } }],
            yAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { beginAtZero: true, fontColor: mutedColor(), fontSize: 11, callback: function (v) { return v + 'm'; } } }]
          },
          tooltips: {
            callbacks: {
              label: function (item) {
                return ' ' + sorted[item.index].conclusion + ' · ' + item.yLabel + ' min';
              }
            }
          }
        }
      });
  
      // Duration trend line
      new Chart(document.getElementById(lineId), {
        type: 'line',
        data: {
          labels: runLabels,
          datasets: [{
            label: 'Duration (min)',
            data: runDurations,
            borderColor: cssVar('--blue') || '#58a6ff',
            backgroundColor: 'rgba(88,166,255,0.08)',
            borderWidth: 2,
            pointRadius: 4,
            pointBackgroundColor: cssVar('--blue') || '#58a6ff',
            fill: true,
            lineTension: 0.3
          }]
        },
        options: {
          responsive: true,
          maintainAspectRatio: true,
          legend: { display: false },
          scales: {
            xAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { fontColor: mutedColor(), fontSize: 11 } }],
            yAxes: [{ gridLines: { color: gridColor(), zeroLineColor: gridColor() }, ticks: { beginAtZero: true, fontColor: mutedColor(), fontSize: 11, callback: function (v) { return v + 'm'; } } }]
          },
          tooltips: { callbacks: { label: function (item) { return ' ' + item.yLabel + ' min'; } } }
        }
      });
  
    }, 0);
  
    // Tab switching
    drawerEl.querySelectorAll('.drawer-tab').forEach(function (btn) {
      btn.addEventListener('click', function () {
        drawerEl.querySelectorAll('.drawer-tab').forEach(function (b) { b.classList.remove('active'); });
        btn.classList.add('active');
        var target = btn.dataset.tab;
        document.getElementById('panel-runs-'   + safeWfName).style.display = target === 'runs'   ? '' : 'none';
        document.getElementById('panel-charts-' + safeWfName).style.display = target === 'charts' ? '' : 'none';
  
        if (target === 'charts') {
          var blocks = document.querySelectorAll('#panel-charts-' + safeWfName + ' .wf-chart-block');
          blocks.forEach(function (block, i) {
            block.classList.remove('chart-visible');
            block.style.animationDelay = (i * 120) + 'ms';
            void block.offsetWidth;
            block.classList.add('chart-visible');
          });
        }
      });
    });
  }
  
  // Log toggle
  
  function toggleLog(panelId, btn) {
    var panel = document.getElementById(panelId);
    if (!panel) return;
    var open = panel.classList.toggle('open');
    btn.textContent = open ? '▼ Hide logs' : '▶ Show logs';
  }
  
  // Data fetch
  
  fetch('stats.json')
    .then(function (r) { return r.json(); })
    .then(function (data) {
      var repo = data.repo || '';
      document.getElementById('footerLink').href        = 'https://github.com/' + repo;
      document.getElementById('footerLink').textContent = repo;
  
      var generatedAt   = data.generated_at ? new Date(data.generated_at) : null;
      var lastUpdatedEl = document.getElementById('lastUpdated');
      if (generatedAt && lastUpdatedEl) {
        var diff    = Math.floor((Date.now() - generatedAt) / 60000);
        var timeStr = generatedAt.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
        var dateStr = generatedAt.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' });
        var agoStr  = diff < 1 ? 'just now' : diff < 60 ? diff + 'm ago' : Math.floor(diff / 60) + 'h ago';
        lastUpdatedEl.textContent = 'updated ' + dateStr + ' ' + timeStr + ' (' + agoStr + ')';
        lastUpdatedEl.title = generatedAt.toISOString();
      }
  
      var health = data.overall_health || 0;
      document.getElementById('healthVal').textContent = health.toFixed(1) + '%';
      var fill = document.getElementById('healthBarFill');
      fill.style.width      = health + '%';
      fill.style.background = healthColor(health);
  
      allWorkflows = data.workflows || [];
      document.getElementById('totalVal').textContent = allWorkflows.length;
      document.getElementById('passVal').textContent  = allWorkflows.filter(function (w) { return w.failure_rate <= 20 && w.total_runs > 0; }).length;
      document.getElementById('failVal').textContent  = allWorkflows.filter(function (w) { return w.failure_rate > 20; }).length;
  
      var WINDOW_MS   = 24 * 60 * 60 * 1000;
      var windowStart = new Date(Date.now() - WINDOW_MS);
      var todayRuns = 0, todayPass = 0, todayFail = 0;
  
      allWorkflows.forEach(function (wf) {
        (wf.recent_runs || []).forEach(function (run) {
          var t = new Date(run.run_started_at || run.created_at);
          if (t < windowStart) return;
          todayRuns++;
          if (run.conclusion === 'success') todayPass++;
          else if (['failure', 'timed_out'].indexOf(run.conclusion) !== -1) todayFail++;
        });
      });
  
      var healthToday = (todayPass + todayFail) > 0 ? (todayPass / (todayPass + todayFail)) * 100 : 0;
      document.getElementById('runsTodayVal').textContent   = todayRuns;
      document.getElementById('passTodayVal').textContent   = todayPass;
      document.getElementById('failTodayVal').textContent   = todayFail;
      document.getElementById('healthTodayVal').textContent = healthToday.toFixed(1) + '%';
      var todayFill = document.getElementById('healthTodayBarFill');
      todayFill.style.width      = healthToday + '%';
      todayFill.style.background = healthColor(healthToday);
  
      if (activeFilter === 'chart') buildCharts(allWorkflows);
      else applyFilters();
    })
    .catch(function (e) {
      document.getElementById('wfBody').innerHTML =
        '<tr class="loading-row"><td colspan="6">Error loading stats.json: ' + e + '</td></tr>';
    });