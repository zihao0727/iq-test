(() => {
  if (window === window.top) return;
  let pending;
  function fit() {
    const root = document.documentElement;
    const body = document.body;
    document.getElementById('preview-fit-style')?.remove();
    const width = Math.max(root.scrollWidth, body?.scrollWidth || 0, innerWidth, 1);
    const height = Math.max(root.scrollHeight, body?.scrollHeight || 0, innerHeight, 1);
    const scale = Math.min(innerWidth / width, innerHeight / height);
    const x = (innerWidth - width * scale) / 2;
    const y = (innerHeight - height * scale) / 2;
    const style = document.createElement('style');
    style.id = 'preview-fit-style';
    style.textContent = `html{overflow:hidden!important;width:${width}px!important;height:${height}px!important;transform-origin:top left!important;transform:translate(${x}px,${y}px) scale(${scale})!important}body{overflow:hidden!important}`;
    (document.head || root).append(style);
  }
  function schedule() {
    cancelAnimationFrame(pending);
    pending = requestAnimationFrame(fit);
  }
  window.addEventListener('load', schedule);
  window.addEventListener('resize', schedule);
  document.fonts?.ready.then(schedule);
  schedule();
})();
