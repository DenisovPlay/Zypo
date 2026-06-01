// js/main.js

function saveOpenTabs() {
  if (typeof tabs !== 'undefined' && typeof activeTabId !== 'undefined') {
    const openUrls = tabs.map(t => {
      try {
        return t.view.getURL() || t.url;
      } catch(e) {
        return t.url;
      }
    });
    const activeIndex = tabs.findIndex(t => t.id === activeTabId);
    localStorage.setItem('zypo_open_tabs_state', JSON.stringify({
      urls: openUrls,
      activeIndex: activeIndex !== -1 ? activeIndex : 0
    }));
    // keep old format
    localStorage.setItem('zypo_open_tabs', JSON.stringify(openUrls));
  }
}

// Periodically save open tabs state
setInterval(saveOpenTabs, 2000);

// Initialize tabs when the browser starts
if (typeof ipcRenderer !== 'undefined') {
  ipcRenderer.invoke('get-settings').then(settings => {
    if (settings && settings.startupAction === 'continue') {
       const stateStr = localStorage.getItem('zypo_open_tabs_state');
       if (stateStr) {
           try {
               const state = JSON.parse(stateStr);
               if (state.urls && state.urls.length > 0) {
                   state.urls.forEach(url => createTab(url));
                   if (state.activeIndex >= 0 && state.activeIndex < tabs.length) {
                       setActiveTab(tabs[state.activeIndex].id);
                   }
               } else {
                   createTab('newtab');
               }
           } catch(e) { createTab('newtab'); }
       } else {
           const savedTabs = JSON.parse(localStorage.getItem('zypo_open_tabs')) || [];
           if (savedTabs.length > 0) {
              savedTabs.forEach(url => createTab(url));
           } else {
              createTab('newtab');
           }
       }
    } else if (settings && settings.startupAction === 'specific') {
       createTab(settings.homePage || 'newtab');
    } else {
       createTab('newtab');
    }
  }).catch(() => {
    createTab('newtab');
  });
} else {
  createTab('newtab');
}
