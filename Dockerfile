FROM golang:1.26-alpine

WORKDIR /app
COPY . /app

RUN go build -o /usr/local/bin/egate .

ENV EGATE_HOST=0.0.0.0
EXPOSE 54283

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:${EGATE_PORT:-54283}/healthz || exit 1

ENTRYPOINT ["egate"]
CMD ["--env", "/app/.env"]
