// js/webview.js

function attachWebviewListeners(webview, id, titleSpan, faviconImg, faviconEmoji) {
  webview.addEventListener('did-start-loading', () => {
    titleSpan.innerText = '...';
    if (faviconImg.style.display === 'block') {
      faviconImg.style.display = 'none';
    }
  });
  
  webview.addEventListener('page-title-updated', (e) => {
    if (e.title && !e.title.includes('://') && e.title !== 'about:blank') {
      const tabBtn = document.getElementById('tab-' + id);
      if (e.title.startsWith('⚠️ ')) {
        titleSpan.innerText = e.title.replace('⚠️ ', '');
        faviconEmoji.innerText = '⚠️';
        faviconEmoji.style.display = 'block';
        faviconImg.style.display = 'none';
        if (tabBtn) tabBtn.classList.add('animate-pulse', 'bg-red-500/25', 'text-red-200');
      } else {
        titleSpan.innerText = e.title;
        if (tabBtn) tabBtn.classList.remove('animate-pulse', 'bg-red-500/25', 'text-red-200');
      }
    }
  });

  webview.addEventListener('page-favicon-updated', (e) => {
    if (e.favicons && e.favicons.length > 0) {
      faviconImg.src = e.favicons[0];
      faviconImg.style.display = 'block';
      if (faviconEmoji) faviconEmoji.style.display = 'none';
    }
  });

  webview.addEventListener('focus', () => {
    try {
      const body = document.querySelector('body');
      if (!body) return;
      const data = Alpine.$data(body);
      if (data) {
        data.networkOpen = false;
        data.menuOpen = false;
        data.userOpen = false;
      }
    } catch(err) {}
  });

  webview.addEventListener('ipc-message', (e) => {
    try {
      const body = document.querySelector('body');
      if (!body) return;
      const data = Alpine.$data(body);
      if (data && e.channel === 'zypo-alert' && typeof data.alert === 'function') {
         data.alert(e.args[0], e.args[1], e.args[2] || 'info');
      }
    } catch(err) {
      console.error('Failed to handle webview alert:', err);
    }
  });

  webview.addEventListener('did-navigate', async (e) => {
    // Add to full history
    setTimeout(() => {
      try {
        const currentTitle = webview.getTitle();
        let displayTitle = currentTitle;
        if (!currentTitle || currentTitle.includes('://') || currentTitle === 'about:blank') {
            displayTitle = e.url.replace('zypo://', '').replace('zb://', '').replace(/\/$/, '');
        }
        if (!e.url.startsWith('zb://') && !e.url.includes('/?q=') && !e.url.includes('/search?q=')) {
            addToFullHistory(e.url, displayTitle);
        }
      } catch(err) {}
    }, 500);

    if (activeTabId === id) {
      let displayUrl = e.url.replace('zypo://', '').replace('zb://', '').replace(/\/$/, '');
      if (displayUrl === 'newtab') addressInput.value = '';
      else addressInput.value = displayUrl;
      updateNavButtons();
      updateBookmarkIconForUrl(e.url);
      
      // Smart title update
      setTimeout(() => {
        try {
          const currentTitle = webview.getTitle();
          if (currentTitle && !currentTitle.includes('://') && currentTitle !== 'about:blank') {
              titleSpan.innerText = currentTitle.startsWith('⚠️ ') ? currentTitle.replace('⚠️ ', '') : currentTitle;
          } else if (titleSpan.innerText === 'Loading...') {
              titleSpan.innerText = displayUrl;
          }
        } catch(err) {}
      }, 100);
    }
  });
}
