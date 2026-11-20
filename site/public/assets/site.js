// Portide 宣传页的共用脚本：阅读进度条、导航栏阴影、回到顶部、步骤切换截图、数字渐显、截图横排翻页、复制命令，
// 以及下载链接。
//
// REPO 填 GitHub 仓库地址（例如 https://github.com/owner/portide），仓库公开前留空：
// 带 data-release 的按钮指向 REPO/releases/latest，带 data-repo 的链接指向仓库里的文件；
// 留空时下载按钮变灰并显示 data-soon 里的文字，仓库链接隐藏。
var REPO = '';

(function () {
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var $ = function (s, el) { return (el || document).querySelector(s); };
  var $$ = function (s, el) { return [].slice.call((el || document).querySelectorAll(s)); };

  // 下载和仓库链接
  $$('[data-release]').forEach(function (a) {
    if (REPO) { a.href = REPO + '/releases/latest'; a.target = '_blank'; a.rel = 'noopener'; return; }
    a.classList.add('off'); a.removeAttribute('href'); a.setAttribute('aria-disabled', 'true');
    if (a.dataset.soon) a.textContent = a.dataset.soon;
  });
  $$('[data-repo]').forEach(function (a) {
    if (!REPO) { a.hidden = true; return; }
    // data-repo="docs/x.md" 指向仓库里的文件；以 / 开头的（如 /issues）直接接在仓库地址后面
    var p = a.dataset.repo;
    a.href = REPO + (!p ? '' : p.charAt(0) === '/' ? p : '/blob/main/' + p);
    a.target = '_blank'; a.rel = 'noopener';
  });

  // 进度条、导航栏、回到顶部
  var nav = $('#nav'), bar = $('#progress'), topBtn = $('#top-btn');
  function onScroll() {
    var y = window.scrollY, h = document.documentElement.scrollHeight - window.innerHeight;
    if (bar) bar.style.transform = 'scaleX(' + (h > 0 ? y / h : 0) + ')';
    if (nav) nav.classList.toggle('scrolled', y > 8);
    if (topBtn) topBtn.classList.toggle('on', y > 900);
  }
  window.addEventListener('scroll', onScroll, { passive: true });
  onScroll();
  if (topBtn) topBtn.addEventListener('click', function () { window.scrollTo({ top: 0, behavior: reduce ? 'auto' : 'smooth' }); });

  // 步骤：每一步换一张 iPad 截图
  var steps = $$('.step'), img = $('#stage-img'), cap = $('#stage-cap');
  function select(i) {
    steps.forEach(function (s, j) { s.setAttribute('aria-selected', String(i === j)); s.tabIndex = i === j ? 0 : -1; });
    img.src = 'img/ipad-' + steps[i].dataset.img + '.jpg';
    img.alt = steps[i].dataset.cap;
    cap.textContent = steps[i].dataset.cap;
  }
  steps.forEach(function (s, i) {
    s.addEventListener('click', function () { select(i); });
    s.addEventListener('keydown', function (e) {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      e.preventDefault();
      var n = (i + (e.key === 'ArrowDown' ? 1 : steps.length - 1)) % steps.length;
      select(n); steps[n].focus();
    });
  });
  if (steps.length && img) select(0);
  // 预先加载其余几张，切换时不闪
  steps.slice(1).forEach(function (s) { var i = new Image(); i.src = 'img/ipad-' + s.dataset.img + '.jpg'; });

  // 滚到眼前时渐显
  var reveals = $$('.reveal');
  if (!reduce && 'IntersectionObserver' in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); } });
    }, { threshold: .25 });
    reveals.forEach(function (el, i) { el.style.transitionDelay = (i % 4) * 90 + 'ms'; io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add('in'); });
  }

  // 截图横排。宽屏上整节固定住，往下滚页面时这一排横向移动，直到最后一张完全露出（鼠标滚轮也能看全）；
  // 窄屏和减少动态效果时改成手动横向滑动，旁边有左右翻页按钮。
  var gallery = $('#gallery'), show = $('#ios'), pin = show && $('.showcase-pin', show);
  var pinned = false, travel = 0;
  function pinTop() { return nav ? nav.offsetHeight + (parseFloat(getComputedStyle(nav).top) || 0) : 0; }
  function layoutShowcase() {
    if (!show) return;
    pinned = !reduce && window.innerWidth > 900;
    show.classList.toggle('pinned', pinned);
    show.style.height = ''; gallery.style.transform = '';
    if (!pinned) return;
    travel = Math.max(0, gallery.offsetWidth - document.documentElement.clientWidth);
    show.style.height = (pin.offsetHeight + travel) + 'px';
    moveShowcase();
  }
  function moveShowcase() {
    if (!pinned || !travel) return;
    var p = Math.min(1, Math.max(0, (pinTop() - show.getBoundingClientRect().top) / travel));
    gallery.style.transform = 'translateX(' + (-p * travel) + 'px)';
  }
  layoutShowcase();
  window.addEventListener('scroll', moveShowcase, { passive: true });
  window.addEventListener('resize', layoutShowcase);
  window.addEventListener('load', layoutShowcase);
  // 上一张 / 下一张：每次移动大约一张 iPad 的宽度。固定模式下按钮改为滚动页面，横排跟着动。
  var navBtns = $$('[data-scroll]');
  function step() { var f = gallery.children; return f.length ? f[0].offsetWidth + 22 : gallery.clientWidth * .7; }
  function galleryPos() {
    if (pinned) return { at: pinTop() - show.getBoundingClientRect().top, max: travel };
    return { at: gallery.scrollLeft, max: gallery.scrollWidth - gallery.clientWidth };
  }
  function updateNav() {
    if (!gallery || !navBtns.length) return;
    var g = galleryPos();
    navBtns.forEach(function (b) { b.disabled = Number(b.dataset.scroll) < 0 ? g.at <= 2 : g.at >= g.max - 2; });
  }
  navBtns.forEach(function (b) {
    b.addEventListener('click', function () {
      var d = Number(b.dataset.scroll) * step(), behavior = reduce ? 'auto' : 'smooth';
      if (!pinned) { gallery.scrollBy({ left: d, behavior: behavior }); return; }
      // 还没滚到这一节时，先把它滚到固定的位置，再往前移一组
      var g = galleryPos(), target = Math.min(g.max, Math.max(0, Math.max(0, g.at) + d));
      window.scrollTo({ top: show.getBoundingClientRect().top + window.scrollY - pinTop() + target, behavior: behavior });
    });
  });
  if (gallery) gallery.addEventListener('scroll', updateNav, { passive: true });
  window.addEventListener('scroll', updateNav, { passive: true });
  window.addEventListener('resize', updateNav);
  window.addEventListener('load', updateNav);
  updateNav();

  // 复制命令
  var copy = $('#copy'), cmd = $('#cmd');
  if (copy && cmd) copy.addEventListener('click', function () {
    var label = copy.textContent;
    var done = function () { copy.textContent = copy.dataset.done; setTimeout(function () { copy.textContent = label; }, 1600); };
    var fallback = function () { var r = document.createRange(); r.selectNodeContents(cmd); var s = getSelection(); s.removeAllRanges(); s.addRange(r); };
    try { navigator.clipboard.writeText(cmd.textContent).then(done, fallback); } catch (e) { fallback(); }
  });
})();
