FROM golang:1.26-alpine AS builder

# Устанавливаем зависимости для сборки CGO (нужно для go-sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Копируем весь монорепозиторий, чтобы работал replace в go.mod
COPY . .

# Переходим в директорию yan и собираем
WORKDIR /app/dot-zypo-yan
RUN CGO_ENABLED=1 GOOS=linux go build -o zypo-yan main.go crawler.go

# Финальный легковесный образ
FROM alpine:latest

# Устанавливаем зависимости для работы sqlite
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

WORKDIR /app

# Копируем бинарник и необходимые ресурсы из builder
COPY --from=builder /app/dot-zypo-yan/zypo-yan .
COPY --from=builder /app/dot-zypo-yan/zypo_config.json .
COPY --from=builder /app/dot-zypo-yan/zypo_sites ./zypo_sites

# Создаем папку для данных и БД
RUN mkdir -p data

# Открываем P2P порт (по умолчанию 9000 для YAN)
EXPOSE 9000

# Точка входа
ENTRYPOINT ["./zypo-yan"]
CMD ["-port", "9000"]
