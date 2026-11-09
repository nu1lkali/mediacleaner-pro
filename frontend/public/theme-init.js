/* 主题引导：在首屏渲染前就绪，避免深色模式闪白。
   独立文件而非内联脚本，以便 CSP 保持 script-src 'self'。 */
(function () {
  try {
    var t = localStorage.getItem("mc-theme") || "system";
    var dark =
      t === "dark" ||
      (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
  } catch (e) {
    /* localStorage 不可用时保持默认浅色 */
  }
})();
