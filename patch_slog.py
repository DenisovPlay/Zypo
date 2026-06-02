import os
import re

dirs = ['dot-zypo-client', 'dot-zypo-server', 'dot-zypo-control', 'dot-zypo-yan']
for d in dirs:
    main_path = os.path.join(d, 'main.go')
    if os.path.exists(main_path):
        with open(main_path, 'r') as f:
            content = f.read()
            
        if 'log/slog' not in content:
            content = content.replace('"log"', '"log"\\n\\t"log/slog"')
            content = content.replace('func main() {', 'func main() {\\n\\tlogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))\\n\\tslog.SetDefault(logger)')
            
            with open(main_path, 'w') as f:
                f.write(content)
