import os
path = 'dot-zypo-control/main.go'
with open(path, 'r') as f:
    c = f.read()
c = c.replace('"log"\\n\\t"log/slog"', '"log"')
c = c.replace('func main() {\\n\\tlogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))\\n\\tslog.SetDefault(logger)', 'func main() {')
with open(path, 'w') as f:
    f.write(c)
