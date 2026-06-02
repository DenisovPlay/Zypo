import os
import re

dirs = ['dot-zypo-client', 'dot-zypo-server', 'dot-zypo-control', 'dot-zypo-yan', 'dot-zypo-common']
for d in dirs:
    for root, _, files in os.walk(d):
        for f in files:
            if f.endswith('.go'):
                path = os.path.join(root, f)
                with open(path, 'r') as file:
                    content = file.read()
                
                # Replace log.Printf(msg, args...) with slog.Info(fmt.Sprintf(msg, args...))
                # Note: this regex is simple, we might just replace "log.Printf(" with "slog.Info(fmt.Sprintf("
                # and then we need to add a closing parenthesis.
                # Better approach:
                if 'log.' in content and '"log"' in content:
                    content = content.replace('"log"', '"log/slog"\n\t"fmt"')
                    
                    def repl_printf(m):
                        # Extract everything inside log.Printf(...)
                        args = m.group(1)
                        # If args contains a format string with %, we use fmt.Sprintf
                        # If not, just slog.Info
                        if '%' in args:
                            return f'slog.Info(fmt.Sprintf({args}))'
                        else:
                            return f'slog.Info({args})'
                    
                    content = re.sub(r'log\.Printf\((.*?)\)', repl_printf, content, flags=re.DOTALL)
                    
                    # log.Println -> slog.Info
                    content = re.sub(r'log\.Println\((.*?)\)', r'slog.Info(\1)', content, flags=re.DOTALL)
                    
                    # log.Fatalf -> slog.Error and os.Exit(1)
                    def repl_fatalf(m):
                        args = m.group(1)
                        if '%' in args:
                            return f'slog.Error(fmt.Sprintf({args})); os.Exit(1)'
                        else:
                            return f'slog.Error({args}); os.Exit(1)'
                            
                    content = re.sub(r'log\.Fatalf\((.*?)\)', repl_fatalf, content, flags=re.DOTALL)
                    
                    # clean up double imports of fmt
                    # (gofmt will fix it anyway if we run goimports)
                    
                    with open(path, 'w') as file:
                        file.write(content)

