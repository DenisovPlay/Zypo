// js/ui.js

btnNewTab.addEventListener('click', () => {
  createTab('newtab');
});
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
  updateBookmarkIconForUrl();
  
  // Refresh New Tab pages if open
  const tabs = document.querySelectorAll('webview');
  tabs.forEach(wv => {
      if(wv.getURL().includes('newtab.html')) wv.reload();
  });
});

function updateBookmarkIconForUrl(targetUrl) {
  let url = targetUrl;
  if (!url) {
    const view = getActiveView();
    if (!view) return;
    url = view.getURL();
  }
  
  const bookmarks = getBookmarks();
  const isBookmarked = bookmarks.some(b => b.url === url);
  
  if (isBookmarked) {
    btnBookmark.classList.replace('text-zinc-400', 'text-yellow-400');
    btnBookmark.innerHTML = '<svg width="20" height="20" fill="currentColor" viewBox="0 0 24 24"><path d="M12 17.27L18.18 21l-1.64-7.03L22 9.24l-7.19-.61L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21z"/></svg>';
  } else {
    btnBookmark.classList.replace('text-yellow-400', 'text-zinc-400');
    btnBookmark.innerHTML = '<svg width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" d="M11.48 3.499a.562.562 0 011.04 0l2.125 5.111a.563.563 0 00.475.345l5.518.442c.499.04.701.663.321.988l-4.204 3.602a.563.563 0 00-.182.557l1.285 5.385a.562.562 0 01-.84.61l-4.725-2.885a.563.563 0 00-.586 0L6.982 20.54a.562.562 0 01-.84-.61l1.285-5.386a.562.562 0 00-.182-.557l-4.204-3.602a.563.563 0 01.321-.988l5.518-.442a.563.563 0 00.475-.345L11.48 3.5z"></path></svg>';
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
