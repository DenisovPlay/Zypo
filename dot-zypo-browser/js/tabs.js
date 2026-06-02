// js/tabs.js

const SYSTEM_ICONS = {
  'newtab': '✨',
  'bookmarks': '⭐',
  'history': '🕒',
  'downloads': '📥',
  'settings': '⚙️',
  'about': 'ℹ️',
  'wallet': '💰',
  'network': '📡',
  'vpn': '🛡️',
  'domains.zypo': '🌐',
  'gov.zypo': '🏛️',
  'docs.zypo': '📚'
};

function createTab(initialUrl = 'newtab') {
  let initialDomain = initialUrl.replace('zb://', '').replace('zypo://', '').split('/')[0];
  const internalPages = ['about', 'newtab', 'bookmarks', 'downloads', 'history', 'settings', 'wallet', 'network', 'vpn'];
  let isInternal = internalPages.includes(initialDomain);
  const fullUrl = isInternal ? `zb://${initialDomain}` : (initialUrl.includes('://') ? initialUrl : `zypo://${initialUrl}`);

  // SINGLETON CHECK: If internal page already open, switch to it
  if (isInternal) {
    const existingTab = tabs.find(t => {
      try {
        const url = t.view.getAttribute('src') || '';
        return url.includes(initialDomain);
      } catch(e) { return false; }
    });
    if (existingTab) {
      setActiveTab(existingTab.id);
      return;
    }
  }

  const id = `tab-${tabCounter++}`;
  
  // Create Tab Button
  const tabBtn = document.createElement('div');
  tabBtn.className = 'tab flex items-center px-4 py-1.5 rounded-2xl border-x border-t cursor-pointer select-none text-sm font-medium transition-all duration-200 group';
  tabBtn.id = `tab-${id}`;
  
  // Middle click to close
  tabBtn.onmousedown = (e) => {
    if (e.button === 1) {
      e.preventDefault();
      closeTab(id);
    }
  };

  // Drag & Drop reordering
  tabBtn.draggable = true;
  tabBtn.ondragstart = (e) => {
    e.dataTransfer.setData('text/plain', id);
    tabBtn.classList.add('opacity-50');
  };
  tabBtn.ondragend = () => {
    tabBtn.classList.remove('opacity-50');
  };
  tabBtn.ondragover = (e) => {
    e.preventDefault();
  };
  tabBtn.ondrop = (e) => {
    e.preventDefault();
    const draggedId = e.dataTransfer.getData('text/plain');
    if (draggedId && draggedId !== id) {
      const draggedTab = document.getElementById(`tab-${draggedId}`);
      if (!draggedTab) return;
      const draggedIndex = tabs.findIndex(t => t.id === draggedId);
      const targetIndex = tabs.findIndex(t => t.id === id);
      if (draggedIndex > -1 && targetIndex > -1) {
        const container = document.getElementById('tabs-container');
        if (draggedIndex < targetIndex) {
          container.insertBefore(draggedTab, tabBtn.nextSibling);
        } else {
          container.insertBefore(draggedTab, tabBtn);
        }
        const [draggedObj] = tabs.splice(draggedIndex, 1);
        tabs.splice(targetIndex, 0, draggedObj);
      }
    }
  };
  
  const faviconContainer = document.createElement('div');
  faviconContainer.className = 'w-4 h-4 mr-2 flex items-center justify-center text-[10px]';
  
  const faviconImg = document.createElement('img');
  faviconImg.className = 'w-4 h-4 rounded-sm';
  faviconImg.style.display = 'none';

  const faviconEmoji = document.createElement('span');
  faviconEmoji.style.display = 'none';

  faviconContainer.appendChild(faviconImg);
  faviconContainer.appendChild(faviconEmoji);

  const titleSpan = document.createElement('span');
  titleSpan.className = 'truncate max-w-[120px]';
  titleSpan.innerText = '...';
  
  const closeBtn = document.createElement('span');
  closeBtn.className = 'ml-2 text-zinc-500 hover:text-white rounded-2xl px-1.5 py-0.5 text-xs opacity-0 group-hover:opacity-100 transition-opacity';
  closeBtn.innerHTML = '✕';
  
  tabBtn.appendChild(faviconContainer);
  tabBtn.appendChild(titleSpan);
  tabBtn.appendChild(closeBtn);
  
  // Set initial icon for internal pages
  if (isInternal && SYSTEM_ICONS[initialDomain]) {
    faviconEmoji.innerText = SYSTEM_ICONS[initialDomain];
    faviconEmoji.style.display = 'block';
  }

  // Create Wrapper
  const wrapper = document.createElement('div');
  wrapper.id = `wrapper-${id}`;
  wrapper.className = 'view-wrapper hidden w-full h-full';

  // Create Webview via HTML to avoid dynamic attribute crashes
  const preloadName = isInternal ? 'preload_internal.js' : 'preload.js';
  const preloadPath = window.electron.getPreloadPath(preloadName);
  wrapper.innerHTML = `<webview id="view-${id}" preload="${preloadPath}" style="display:inline-flex; width:100%; height:100%;"></webview>`;
  const webview = wrapper.querySelector('webview');
  
  // Attach listeners
  attachWebviewListeners(webview, id, titleSpan, faviconImg, faviconEmoji);

  // Event Listeners for Tab
  tabBtn.addEventListener('click', (e) => {
    if (e.target !== closeBtn) {
      setActiveTab(id);
    }
  });
  
  closeBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closeTab(id);
  });

  // Append
  tabsContainer.insertBefore(tabBtn, btnNewTab);
  webviewsContainer.appendChild(wrapper);
  
  // Set src after DOM attachment
  webview.setAttribute('src', fullUrl);
  
  tabs.push({ id, btn: tabBtn, view: webview, wrapper: wrapper, title: titleSpan, faviconImg, faviconEmoji, url: fullUrl });
  setActiveTab(id);
}

function setActiveTab(id) {
  activeTabId = id;
  
  tabs.forEach(tab => {
    if (tab.id === id) {
      tab.btn.classList.add('bg-[#18181b]', 'border-zinc-700', 'text-white', 'rounded-b-none');
      tab.btn.classList.remove('bg-transparent', 'border-transparent', 'text-zinc-500');
      tab.wrapper.classList.remove('hidden');
      
      try {
        let currentUrl = tab.view.getURL();
        if (currentUrl) {
          let displayUrl = currentUrl.replace('zypo://', '').replace('zb://', '').replace(/\/$/, '');
          if (displayUrl === 'newtab') addressInput.value = '';
          else addressInput.value = displayUrl;
          updateBookmarkIconForUrl(currentUrl);
        } else {
          updateBookmarkIconForUrl('');
        }
      } catch (e) {}
    } else {
      tab.btn.classList.remove('bg-[#18181b]', 'border-zinc-700', 'text-white', 'rounded-b-none');
      tab.btn.classList.add('bg-transparent', 'border-transparent', 'text-zinc-500');
      tab.wrapper.classList.add('hidden');
    }
  });
  updateNavButtons();
}

function closeTab(id) {
  const index = tabs.findIndex(t => t.id === id);
  if (index === -1) return;
  
  const tab = tabs[index];
  if (tab.url && tab.url !== 'zb://newtab' && tab.url !== 'about:blank') {
    closedTabsHistory.push(tab.url);
    if (closedTabsHistory.length > 50) closedTabsHistory.shift();
  }
  tab.btn.remove();
  
  // Stop any pending navigations/loads to prevent Electron webview internal crashes
  const webview = tab.wrapper.querySelector('webview');
  if (webview) {
    try {
      webview.stop();
    } catch(e) {}
  }
  
  tab.wrapper.remove();
  tabs.splice(index, 1);
  
  if (tabs.length === 0) {
    createTab('newtab'); // Always keep at least 1 tab
  } else if (activeTabId === id) {
    // Switch to adjacent tab
    setActiveTab(tabs[Math.max(0, index - 1)].id);
  }
}
