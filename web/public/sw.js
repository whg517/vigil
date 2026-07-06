/*
 * Vigil PWA Service Worker（最简 app-shell 缓存，P4·B3）。
 *
 * 目标：让 Vigil 可「安装到桌面」并在离线时仍能加载壳（大屏挂墙场景网络偶断不白屏）。
 * 刻意保持极简（手写而非引 vite-plugin-pwa，避免构建期依赖与 lockfile 变动）：
 *   · install：预缓存最小 app-shell（根文档 + manifest + 图标）。
 *   · activate：清理旧版本缓存。
 *   · fetch：
 *       - /api 请求（含 WS 升级前的 HTTP、REST）一律**不经 SW**（直连网络）——
 *         告警数据必须实时且带鉴权，绝不返回过期缓存。
 *       - 导航请求（HTML）走 network-first，失败回退缓存的 index.html（离线壳）。
 *       - 其它同源静态资源（JS/CSS/图标，带 hash 文件名）走 cache-first，加速加载。
 *
 * 注意：不缓存 API 响应，避免离线态展示陈旧告警状态误导值班人。
 */
const CACHE = "vigil-shell-v1";
const SHELL = ["/", "/index.html", "/manifest.webmanifest", "/favicon.svg"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") return;

  const url = new URL(request.url);
  // 跨域 / API / WS：不拦截，直连网络（数据实时 + 带鉴权，不进 SW 缓存）。
  if (url.origin !== self.location.origin || url.pathname.startsWith("/api")) {
    return;
  }

  // 导航请求（SPA 文档）：network-first，离线回退缓存的 index.html（app-shell）。
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request).catch(() => caches.match("/index.html").then((r) => r || caches.match("/"))),
    );
    return;
  }

  // 其它静态资源：cache-first（命中即返回，未命中拉网络并回填缓存）。
  event.respondWith(
    caches.match(request).then(
      (cached) =>
        cached ||
        fetch(request).then((resp) => {
          // 只缓存成功的基本响应（避免缓存不透明/错误响应）。
          if (resp && resp.status === 200 && resp.type === "basic") {
            const copy = resp.clone();
            caches.open(CACHE).then((cache) => cache.put(request, copy));
          }
          return resp;
        }),
    ),
  );
});
