FROM golang:1.26-alpine

WORKDIR /app
COPY . /app

RUN go build -o /usr/local/bin/egate .

ENV EGATE_HOST=0.0.0.0
EXPOSE 11111
ENTRYPOINT ["egate"]
CMD ["--env", "/app/.env"]
