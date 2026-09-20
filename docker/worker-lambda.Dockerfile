FROM --platform=linux/amd64 golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags lambda.norpc -ldflags="-s -w" -o /out/wordflow-worker ./cmd/lambda-worker

FROM --platform=linux/amd64 alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/wordflow-worker ./wordflow-worker
ENTRYPOINT ["/app/wordflow-worker"]
