FROM golang:1.22-alpine3.20 AS builder
WORKDIR /go/src/app
COPY . .
RUN CGO_ENABLED=0 go build -o vault-init .

FROM alpine:3.20
RUN apk upgrade --no-cache
COPY --from=builder /go/src/app/vault-init /usr/local/bin
ENTRYPOINT ["vault-init"]
