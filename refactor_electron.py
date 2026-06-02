import os
import re

directories = ['dot-zypo-browser/pages', 'dot-zypo-browser/js', 'dot-zypo-browser']

for d in directories:
    for filename in os.listdir(d):
        if filename.endswith('.html') or filename.endswith('.js'):
            filepath = os.path.join(d, filename)
            if os.path.isfile(filepath) and filename != 'preload.js':
                with open(filepath, 'r') as f:
                    content = f.read()

                # Replace const { ipcRenderer } = require('electron')
                content = re.sub(r'const\s*\{\s*ipcRenderer\s*\}\s*=\s*require\([\'"]electron[\'"]\);?', 'const { ipcRenderer } = window.electron;', content)
                # Replace const electron = require('electron')
                content = re.sub(r'const\s*electron\s*=\s*require\([\'"]electron[\'"]\);?', 'const electron = window.electron;', content)
                # Replace const { shell } = require('electron')
                content = re.sub(r'const\s*\{\s*shell\s*\}\s*=\s*require\([\'"]electron[\'"]\);?', 'const { shell } = window.electron;', content)
                
                with open(filepath, 'w') as f:
                    f.write(content)
