FROM golang:1.21
WORKDIR /go/src/app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o vault-init -v .

FROM scratch
COPY --from=0 /go/src/app/vault-init /
ENTRYPOINT ["/vault-init"]
