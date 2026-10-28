FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend ./cmd/backend

FROM alpine:3.21
# CA certificates: MongoDB Atlas and the market-data calls are all TLS.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
USER app
COPY --from=build /out/backend /backend
# Settings (MONGODB_URI, API_SECRET, PORT, ...) come from the environment at run time, never from the image.
EXPOSE 8081
ENTRYPOINT ["/backend"]
