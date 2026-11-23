// 下载页：认出访客的系统，把对应的下载放在最前面；仓库公开后（site.js 里的 REPO）从 GitHub 读最新版本，
// 给每一行填上真实的文件名、大小和下载地址。页面上的文字都在 HTML 的 data-* 属性里，这里不写死任何语言。
(function () {
  var $ = function (s, el) { return (el || document).querySelector(s); };
  var $$ = function (s, el) { return [].slice.call((el || document).querySelectorAll(s)); };
  var page = $('#dl');
  if (!page) return;
  var t = page.dataset;
  var primary = $('#primary');

  // 访客的系统。iPadOS 的 Safari 自称 Mac，用触点数区分。
  var ua = navigator.userAgent, os = null;
  if (/iPhone|iPod/.test(ua) || (/iPad/.test(ua)) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) os = 'ios';
  else if (/Android/.test(ua)) os = 'android';
  else if (/Macintosh|Mac OS X/.test(ua)) os = 'mac';
  else if (/Windows/.test(ua)) os = 'windows';
  else if (/Linux|X11|CrOS/.test(ua)) os = 'linux';
  var macArch = 'arm64'; // Safari 不告诉网页芯片类型，默认 Apple 芯片；Intel 那一行就在旁边

  // 每个系统首选哪一行
  function preferred() {
    return { mac: 'mac-' + macArch, windows: 'win-setup', linux: 'linux-deb' }[os];
  }
  function row(key) { return $('[data-key="' + key + '"]'); }

  // 标出"你的设备"那张卡片
  var card = os && $('#dl-' + (os === 'android' ? 'none' : os));
  if (card) {
    card.classList.add('mine');
    var tag = document.createElement('span'); tag.className = 'mine-tag'; tag.textContent = t.mine; card.appendChild(tag);
  }

  function setPrimary() {
    if (!primary) return;
    var label = primary.dataset[os || 'other'] || primary.dataset.other;
    var r = preferred() && row(preferred());
    var a = r && $('.btn', r);
    if (os === 'ios' && primary.dataset.appstore) {
      primary.textContent = label; primary.href = primary.dataset.appstore; primary.target = '_blank'; primary.rel = 'noopener'; primary.classList.remove('off'); return;
    }
    if (os === 'ios' || os === 'android') {
      primary.textContent = label; primary.classList.add('off'); primary.removeAttribute('href'); return;
    }
    if (a && a.href && !a.classList.contains('off')) {
      primary.textContent = label; primary.href = a.href; primary.classList.remove('off');
      return;
    }
    if (!REPO && preferred()) { primary.textContent = t.soon; primary.classList.add('off'); primary.removeAttribute('href'); return; }
    primary.textContent = primary.dataset.other; primary.href = '#all'; primary.classList.remove('off');
  }

  // 有些浏览器能告诉网页芯片类型
  if (os === 'mac' && navigator.userAgentData && navigator.userAgentData.getHighEntropyValues) {
    navigator.userAgentData.getHighEntropyValues(['architecture']).then(function (v) {
      if (v.architecture === 'x86') { macArch = 'amd64'; setPrimary(); }
    }, function () {});
  }

  var buttons = $$('[data-asset] .btn');
  if (!REPO) {
    buttons.forEach(function (b) { b.classList.add('off'); b.removeAttribute('href'); b.textContent = t.soon; });
    $$('[data-needs-repo]').forEach(function (el) { el.hidden = true; });
    setPrimary();
    return;
  }

  var m = REPO.match(/github\.com\/([^/]+)\/([^/#?]+)/);
  var owner = m ? m[1] : '', repo = m ? m[2].replace(/\.git$/, '') : '';
  $$('[data-image]').forEach(function (el) { el.textContent = 'ghcr.io/' + owner.toLowerCase() + '/portide-relay:latest'; });
  // 读不到 GitHub 时（网络、频率限制）退回到发布页面，让访客自己挑
  buttons.forEach(function (b) { b.href = REPO + '/releases/latest'; b.target = '_blank'; b.rel = 'noopener'; });
  setPrimary();

  function size(n) { return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB'; }
  fetch('https://api.github.com/repos/' + owner + '/' + repo + '/releases/latest', { headers: { Accept: 'application/vnd.github+json' } })
    .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
    .then(function (rel) {
      var assets = rel.assets || [];
      $$('[data-asset]').forEach(function (r) {
        var re = new RegExp(r.dataset.asset), a = assets.filter(function (x) { return re.test(x.name); })[0];
        var b = $('.btn', r), meta = $('.meta', r);
        if (!a) { b.classList.add('off'); b.removeAttribute('href'); b.textContent = t.missing; return; }
        b.href = a.browser_download_url; b.removeAttribute('target');
        if (meta) meta.textContent = a.name + ' · ' + size(a.size);
      });
      var sums = assets.filter(function (x) { return x.name === 'SHA256SUMS.txt'; })[0];
      $$('[data-sums]').forEach(function (el) { if (sums) el.href = sums.browser_download_url; else el.hidden = true; });
      var line = $('#release-line');
      if (line) {
        var d = new Date(rel.published_at || rel.created_at);
        var date = isNaN(d) ? '' : d.toLocaleDateString(document.documentElement.lang, { year: 'numeric', month: 'long', day: 'numeric' });
        line.innerHTML = '';
        var v = document.createElement('span'); v.textContent = t.relTpl.replace('{v}', (rel.tag_name || '').replace(/^v/, '')).replace('{d}', date);
        var notes = document.createElement('a'); notes.href = rel.html_url; notes.target = '_blank'; notes.rel = 'noopener'; notes.textContent = t.notes;
        line.appendChild(v); line.appendChild(notes);
      }
      setPrimary();
    })
    .catch(function () { /* 保持指向发布页面 */ });
})();
