// js/ui.js

// Back/Forward/Refresh Buttons
btnBack.addEventListener('click', () => {
  const view = getActiveView();
  if (view && view.canGoBack()) view.goBack();
});

btnForward.addEventListener('click', () => {
  const view = getActiveView();
  if (view && view.canGoForward()) view.goForward();
});

btnReload.addEventListener('click', () => {
  const view = getActiveView();
  if (view) view.reload();
});

btnHome.addEventListener('click', () => {
  navigate(getSettings().homePage || 'zb://newtab');
});

// Bookmark Toggle
btnBookmark.addEventListener('click', () => {
  const view = getActiveView();
  if (!view) return;
  const url = view.getURL();
  const title = view.getTitle();
  
  const bookmarks = getBookmarks();
  const index = bookmarks.findIndex(b => b.url === url);
  
  if (index >= 0) {
    bookmarks.splice(index, 1);
  } else {
    bookmarks.push({ url, title });
  }
  
  saveBookmarks(bookmarks);
  updateBookmarkUI();
  
  // Refresh New Tab pages if open
  const tabs = document.querySelectorAll('webview');
  tabs.forEach(wv => {
      if(wv.getURL().includes('newtab.html')) wv.reload();
  });
});

function updateBookmarkUI() {
  const view = getActiveView();
  if (!view) return;
  const url = view.getURL();
  const bookmarks = getBookmarks();
  const isBookmarked = bookmarks.some(b => b.url === url);
  
  if (isBookmarked) {
    btnBookmark.classList.replace('text-zinc-400', 'text-yellow-400');
    // SVG switch to filled star
    btnBookmark.innerHTML = '<svg class="w-4 h-4" fill="currentColor" viewBox="0 0 24 24"><path d="M12 17.27L18.18 21l-1.64-7.03L22 9.24l-7.19-.61L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21z"/></svg>';
  } else {
    btnBookmark.classList.replace('text-yellow-400', 'text-zinc-400');
    // SVG switch to outlined star
    btnBookmark.innerHTML = '<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11.049 2.927c.3-.921 1.603-.921 1.902 0l1.519 4.674a1 1 0 00.95.69h4.915c.969 0 1.371 1.24.588 1.81l-3.976 2.888a1 1 0 00-.363 1.118l1.518 4.674c.3.922-.755 1.688-1.538 1.118l-3.976-2.888a1 1 0 00-1.176 0l-3.976 2.888c-.783.57-1.838-.197-1.538-1.118l1.518-4.674a1 1 0 00-.363-1.118l-3.976-2.888c-.784-.57-.38-1.81.588-1.81h4.914a1 1 0 00.951-.69l1.519-4.674z" /></svg>';
  }
}

// Menu Item Clicks
document.getElementById('menu-wallet')?.addEventListener('click', () => {
  createTab('wallet');
});

document.getElementById('menu-network')?.addEventListener('click', () => {
  createTab('network');
});

document.getElementById('menu-bookmarks')?.addEventListener('click', () => {
  createTab('bookmarks');
});

document.getElementById('menu-history')?.addEventListener('click', () => {
  createTab('history');
});

document.getElementById('menu-downloads')?.addEventListener('click', () => {
  createTab('downloads');
});

document.getElementById('menu-settings')?.addEventListener('click', () => {
  createTab('settings');
});

document.getElementById('menu-about')?.addEventListener('click', () => {
  createTab('about');
});
