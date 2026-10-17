(() => {
  'use strict';

  for (const root of document.querySelectorAll('.agent-character')) {
    const canvas = root.querySelector('canvas');
    const context = canvas.getContext('2d');
    if (!context) continue;

    let manifest;
    try { manifest = JSON.parse(root.dataset.manifest); }
    catch { continue; }

    const name = root.dataset.name;
    context.imageSmoothingEnabled = !manifest.pixel;

    const fallback = root.querySelector('.character-fallback');
    const controls = root.querySelector('.character-controls');
    const caption = root.querySelector('.character-caption');
    const pause = root.querySelector('.character-pause');
    const actions = root.querySelectorAll('[data-action]');
    const motionPreference = matchMedia('(prefers-reduced-motion: reduce)');
    const sheet = new Image();
    caption.textContent = manifest.animations.idle.caption;
    let action = 'idle';
    let frame = 0;
    let elapsed = 0;
    let lastTime = 0;
    let request = 0;
    let ready = false;
    let inView = true;
    let paused = motionPreference.matches;

    function render() {
      if (!ready) return;
      const animation = manifest.animations[action];
      context.clearRect(0, 0, canvas.width, canvas.height);
      context.drawImage(sheet,
        frame * manifest.frameWidth, animation.row * manifest.frameHeight,
        manifest.frameWidth, manifest.frameHeight,
        0, 0, canvas.width, canvas.height);
    }

    function tick(now) {
      request = 0;
      if (paused || !inView || document.hidden || !ready) {
        lastTime = 0;
        return;
      }
      if (lastTime) elapsed += Math.min(now - lastTime, 100);
      lastTime = now;
      const animation = manifest.animations[action];
      let changed = false;
      while (elapsed >= animation.frameDurationsMs[frame]) {
        elapsed -= animation.frameDurationsMs[frame];
        frame = (frame + 1) % animation.frames;
        changed = true;
      }
      if (changed) render();
      request = requestAnimationFrame(tick);
    }

    function syncPlayback() {
      cancelAnimationFrame(request);
      request = 0;
      lastTime = 0;
      pause.textContent = paused ? '播放动画' : '暂停动画';
      pause.setAttribute('aria-label', (paused ? '播放' : '暂停') + ' ' + name + ' 动画');
      if (ready && !paused && inView && !document.hidden) request = requestAnimationFrame(tick);
    }

    for (const button of actions) {
      button.addEventListener('click', () => {
        action = button.dataset.action;
        const animation = manifest.animations[action];
        frame = 0;
        elapsed = 0;
        for (const other of actions) other.setAttribute('aria-pressed', String(other === button));
        caption.textContent = animation.caption;
        canvas.setAttribute('aria-label', name + '：' + animation.label);
        render();
        syncPlayback();
      });
    }
    pause.addEventListener('click', () => { paused = !paused; syncPlayback(); });
    motionPreference.addEventListener('change', event => {
      paused = event.matches;
      syncPlayback();
    });
    document.addEventListener('visibilitychange', syncPlayback);
    if ('IntersectionObserver' in window) {
      const observer = new IntersectionObserver(entries => {
        inView = entries[0].isIntersecting;
        syncPlayback();
      });
      observer.observe(root);
    }

    function showFallback() {
      ready = false;
      canvas.hidden = true;
      fallback.hidden = false;
      controls.hidden = true;
      caption.textContent = '动作暂未加载，先看看我的头像。';
      syncPlayback();
    }

    sheet.onload = () => {
      if (sheet.naturalWidth !== manifest.columns * manifest.frameWidth ||
          sheet.naturalHeight !== manifest.rows * manifest.frameHeight) {
        showFallback();
        return;
      }
      ready = true;
      render();
      canvas.hidden = false;
      fallback.hidden = true;
      controls.hidden = false;
      syncPlayback();
    };
    sheet.onerror = showFallback;
    sheet.src = manifest.atlas;
  }
})();
