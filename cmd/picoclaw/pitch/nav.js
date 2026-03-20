// Pitch deck navigation — keyboard + swipe + progress
(function () {
  const slides = [
    'index.html', 'slide-2.html', 'slide-3.html',
    'slide-4.html', 'slide-5.html'
  ];
  const path = location.pathname.split('/').pop() || 'index.html';
  const idx = slides.indexOf(path);

  // Progress bar
  const bar = document.createElement('div');
  bar.className = 'nav-progress';
  const fill = document.createElement('div');
  fill.className = 'nav-progress-fill';
  fill.style.width = ((idx + 1) / slides.length * 100) + '%';
  bar.appendChild(fill);
  document.querySelector('nav').appendChild(bar);

  // Keyboard arrows
  document.addEventListener('keydown', function (e) {
    if (e.key === 'ArrowRight' && idx < slides.length - 1) {
      location.href = slides[idx + 1];
    } else if (e.key === 'ArrowLeft' && idx > 0) {
      location.href = slides[idx - 1];
    }
  });

  // Touch swipe
  var startX = 0;
  document.addEventListener('touchstart', function (e) {
    startX = e.changedTouches[0].clientX;
  }, { passive: true });
  document.addEventListener('touchend', function (e) {
    var dx = e.changedTouches[0].clientX - startX;
    if (Math.abs(dx) < 60) return;
    if (dx < 0 && idx < slides.length - 1) {
      location.href = slides[idx + 1];
    } else if (dx > 0 && idx > 0) {
      location.href = slides[idx - 1];
    }
  }, { passive: true });
})();
