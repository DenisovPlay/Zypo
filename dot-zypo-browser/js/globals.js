// js/globals.js
const { ipcRenderer } = window.electron;

window.addEventListener('error', e => {
  ipcRenderer.send('log-error', (e.error ? e.error.stack : e.message) + '\\n');
});
// Global Shortcuts
window.addEventListener('keydown', (e) => {
  // New tab: Cmd+T / Ctrl+T
  if ((e.metaKey || e.ctrlKey) && !e.shiftKey && e.key.toLowerCase() === 't') {
    e.preventDefault();
    createTab();
  }
  // Restore tab: Cmd+Shift+T / Ctrl+Shift+T
  if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 't') {
    e.preventDefault();
    if (typeof closedTabsHistory !== 'undefined' && closedTabsHistory.length > 0) {
      const lastUrl = closedTabsHistory.pop();
      createTab(lastUrl);
    } else {
      createTab();
    }
  }
  // Close tab: Cmd+W / Ctrl+W
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'w') {
    e.preventDefault();
    if (activeTabId) closeTab(activeTabId);
  }
  // Focus address bar: Cmd+L / Ctrl+L
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'l') {
    e.preventDefault();
    const addressInput = document.getElementById('address-input');
    if (addressInput) {
      addressInput.focus();
      addressInput.select();
    }
  }
});
window.addEventListener('unhandledrejection', e => {
  ipcRenderer.send('log-error', (e.reason ? e.reason.stack : String(e.reason)) + '\\n');
});

const tabsContainer = document.getElementById('tabs-container');
const webviewsContainer = document.getElementById('views-container');
const addressInput = document.getElementById('address-input');
const btnBack = document.getElementById('btn-back');
const btnForward = document.getElementById('btn-forward');
const btnRefresh = document.getElementById('btn-refresh');
const btnNewTab = document.getElementById('btn-new-tab');
const btnNetwork = document.getElementById('btn-network');
const autocompleteDropdown = document.getElementById('autocomplete-dropdown');
const autocompleteList = document.getElementById('autocomplete-list');

let historyArr = JSON.parse(localStorage.getItem('zypo_history')) || ['about', 'newtab'];
let tabs = [];
let activeTabId = null;
let tabCounter = 0;
let closedTabsHistory = [];

function saveToHistory(domain) {
  if (domain && !historyArr.includes(domain)) {
    historyArr.push(domain);
    localStorage.setItem('zypo_history', JSON.stringify(historyArr));
  }
}

function addToFullHistory(url, title) {
  ipcRenderer.send('add-history', { url, title });
}

function getActiveView() {
  if (!activeTabId) return null;
  const tab = tabs.find(t => t.id === activeTabId);
  return tab ? tab.view : null;
}

function updateNavButtons() {
  const view = getActiveView();
  if (!view) return;
  try {
    btnBack.style.opacity = view.canGoBack() ? '1' : '0.5';
    btnForward.style.opacity = view.canGoForward() ? '1' : '0.5';
  } catch (e) {}
}
