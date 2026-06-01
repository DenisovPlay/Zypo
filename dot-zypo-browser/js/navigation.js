// js/navigation.js

// --- Autocomplete Logic ---
let autocompleteController = null;

function t(key, def) {
    try {
        const body = document.querySelector('body');
        if (body && window.Alpine) {
            const data = window.Alpine.$data(body);
            if (data && typeof data.t === 'function') {
                return data.t(key) || def;
            }
        }
    } catch (e) {}
    return def;
}

const internalPages = ['about', 'newtab', 'bookmarks', 'network', 'downloads', 'history', 'settings', 'vpn', 'wallet'];

function showAutocomplete(val) {
  autocompleteList.innerHTML = '';
  if (!val) {
    autocompleteDropdown.classList.add('opacity-0');
    setTimeout(() => autocompleteDropdown.classList.add('hidden'), 200);
    return;
  }
  
  let totalItems = 0;

  // 1. Suggest Internal Browser Pages (zb://)
  if (val.toLowerCase().startsWith('zb://') || 'zb://'.startsWith(val.toLowerCase()) || internalPages.some(p => p.includes(val.toLowerCase()))) {
    const searchPart = val.toLowerCase().replace('zb://', '');
    const matches = internalPages.filter(p => p.includes(searchPart));
    
    matches.forEach(p => {
      const li = document.createElement('li');
      li.className = 'px-4 py-2 hover:bg-zinc-800 cursor-pointer text-zinc-300 transition-colors flex items-center justify-between group';
      li.innerHTML = `
        <div class="flex items-center gap-3">
            <svg class="w-4 h-4 text-zinc-500 group-hover:text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.747 0 3.332.477 4.5 1.253v13C19.832 18 18.247 18.477 16.5 18c-1.746 0-3.332.477-4.5 1.253"></path></svg>
            <span class="text-sm font-medium">zb://${p}</span>
        </div>
        <span class="text-[8px] uppercase font-black text-blue-500 bg-blue-500/10 px-1.5 py-0.5 rounded tracking-widest">${t('browser.label_internal', 'Internal')}</span>
      `;
      li.dataset.url = 'zb://' + p;
      li.onmousedown = (e) => {
        e.preventDefault();
        addressInput.value = li.dataset.url;
        triggerNavigation(li.dataset.url);
        hideAutocomplete();
      };
      autocompleteList.appendChild(li);
      totalItems++;
    });
  }

  // 2. Suggest from History
  const cleanVal = val.replace('zypo://', '').replace('zb://', '');
  const historyMatches = historyArr.filter(h => h.includes(cleanVal)).slice(0, 3);
  if (historyMatches.length > 0) {
    historyMatches.forEach(m => {
      const li = document.createElement('li');
      li.className = 'px-4 py-2 hover:bg-zinc-800 cursor-pointer text-zinc-300 transition-colors flex items-center justify-between group border-t border-zinc-800/50';
      li.innerHTML = `<span>${m}</span> <span class="text-[8px] uppercase font-bold text-zinc-500 bg-zinc-800 px-1.5 py-0.5 rounded">${t('browser.label_history', 'History')}</span>`;
      li.dataset.url = m;
      li.onmousedown = (e) => {
        e.preventDefault();
        addressInput.value = m;
        triggerNavigation(m);
        hideAutocomplete();
      };
      autocompleteList.appendChild(li);
      totalItems++;
    });
  }

  if (totalItems > 0) {
    autocompleteDropdown.classList.remove('hidden');
    setTimeout(() => autocompleteDropdown.classList.remove('opacity-0'), 10);
  }

  if (autocompleteController) autocompleteController.abort();
  autocompleteController = new AbortController();
  const signal = autocompleteController.signal;

  const engine = (window.SETTINGS && window.SETTINGS.searchEngine) ? window.SETTINGS.searchEngine : 'ya.rus';
  const enableSearch = (window.SETTINGS && window.SETTINGS.enableSearch !== undefined) ? window.SETTINGS.enableSearch : true;

  if (!enableSearch || val.startsWith('zb://')) return;

  fetch(`zypo://${engine}/api/search?q=${encodeURIComponent(cleanVal)}`, { signal })
    .then(res => res.json())
    .then(data => {
       const resList = data.results ? data.results : (Array.isArray(data) ? data : []);
       if (resList && resList.length > 0) {
          autocompleteDropdown.classList.remove('hidden');
          setTimeout(() => autocompleteDropdown.classList.remove('opacity-0'), 10);
          
          const topResults = resList.slice(0, 5);
          topResults.forEach(res => {
             const li = document.createElement('li');
             li.className = 'px-4 py-2 hover:bg-zinc-800 cursor-pointer transition-colors flex flex-col border-t border-zinc-800/50 group';
             li.dataset.url = res.url;
             
             li.innerHTML = `
               <div class="flex items-center justify-between">
                 <span class="text-white font-medium truncate max-w-[70%] group-hover:text-blue-400 transition-colors">${res.title || res.url}</span>
                 <span class="text-[8px] uppercase font-bold text-blue-500/80 bg-blue-500/10 px-1.5 py-0.5 rounded shrink-0 tracking-widest">${engine}</span>
               </div>
               <div class="text-[10px] text-zinc-500 truncate mt-0.5">${res.url}</div>
             `;
             
             li.onmousedown = (e) => {
               e.preventDefault();
               addressInput.value = res.url;
               triggerNavigation(res.url);
               hideAutocomplete();
             };
             autocompleteList.appendChild(li);
             totalItems++;
          });
       } else if (totalItems === 0) {
          hideAutocomplete();
       }
    })
    .catch(err => {
       if (totalItems === 0) hideAutocomplete();
    });
}

function hideAutocomplete() {
    autocompleteDropdown.classList.add('opacity-0');
    setTimeout(() => autocompleteDropdown.classList.add('hidden'), 200);
}

// --- Smart Address Input ---
addressInput.addEventListener('focus', () => {
  let val = addressInput.value;
  
  // Logic to show protocols on focus
  if (val && !val.startsWith('zypo://') && !val.startsWith('zb://')) {
    if (internalPages.includes(val)) {
        addressInput.value = 'zb://' + val;
    } else if (val.includes('.') && !val.includes(' ')) {
        addressInput.value = 'zypo://' + val;
    }
  }
  
  autocompleteIndex = -1;
  showAutocomplete(addressInput.value);
});

addressInput.addEventListener('blur', () => {
  let val = addressInput.value;
  // Hide protocols on blur for cleaner look
  if (val.startsWith('zypo://')) {
    addressInput.value = val.replace('zypo://', '');
  } else if (val.startsWith('zb://')) {
    addressInput.value = val.replace('zb://', '');
  }
  hideAutocomplete();
});

addressInput.addEventListener('input', (e) => {
  autocompleteIndex = -1;
  showAutocomplete(e.target.value);
});

let autocompleteIndex = -1;

addressInput.addEventListener('keydown', (e) => {
  const items = autocompleteList.querySelectorAll('li');
  
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    if (items.length > 0) {
      autocompleteIndex = (autocompleteIndex + 1) % items.length;
      updateAutocompleteSelection(items);
    }
  } else if (e.key === 'ArrowUp') {
    e.preventDefault();
    if (items.length > 0) {
      autocompleteIndex = (autocompleteIndex - 1 + items.length) % items.length;
      updateAutocompleteSelection(items);
    }
  } else if (e.key === 'Enter') {
    let targetUrl = addressInput.value;
    if (autocompleteIndex >= 0 && autocompleteIndex < items.length) {
      targetUrl = items[autocompleteIndex].dataset.url;
    }
    hideAutocomplete();
    triggerNavigation(targetUrl);
  }
});

function updateAutocompleteSelection(items) {
  items.forEach((item, idx) => {
    if (idx === autocompleteIndex) {
      item.classList.add('bg-zinc-800');
    } else {
      item.classList.remove('bg-zinc-800');
    }
  });
}

function triggerNavigation(url) {
  let finalUrl = url.trim();
  const engine = (window.SETTINGS && window.SETTINGS.searchEngine) ? window.SETTINGS.searchEngine : 'ya.rus';
  const enableSearch = (window.SETTINGS && window.SETTINGS.enableSearch !== undefined) ? window.SETTINGS.enableSearch : true;
  
  if (internalPages.includes(finalUrl.replace('zb://', ''))) {
    finalUrl = 'zb://' + finalUrl.replace('zb://', '');
  } else if (!finalUrl.startsWith('zypo://') && !finalUrl.startsWith('zb://')) {
    if (!enableSearch || (finalUrl.includes('.') && !finalUrl.includes(' '))) {
      finalUrl = 'zypo://' + finalUrl;
    } else {
      finalUrl = `zypo://${engine}/?q=${encodeURIComponent(finalUrl)}`;
    }
  }
  
  const view = getActiveView();
  if (view) {
    view.setAttribute('src', finalUrl);
    addressInput.blur();
    
    // Logic for what to show in input bar after navigation
    let displayVal = finalUrl;
    
    // Protocols hide on blur.
    displayVal = displayVal.replace('zypo://', '').replace('zb://', '');

    if (displayVal.startsWith(`${engine}/?q=`) || displayVal.startsWith(`${engine}/search?q=`)) {
       try {
         const dummy = new URL('http://' + displayVal.replace(`${engine}/?q=`, `${engine}/search?q=`));
         const q = dummy.searchParams.get('q');
         if (q) displayVal = q;
       } catch(e) {}
    }

    if (displayVal === 'newtab') addressInput.value = '';
    else addressInput.value = displayVal;
    
    if (!internalPages.includes(displayVal) && !displayVal.startsWith(`${engine}/?q=`) && !displayVal.startsWith(`${engine}/search?q=`)) {
      saveToHistory(displayVal);
    }
  }
}
