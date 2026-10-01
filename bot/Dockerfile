# gatherbot: a static Go binary on a distroless base. Build from this directory:
#   docker build -t gatherbot .
#   docker run --env-file .env -p 8080:8080 gatherbot
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gatherbot ./cmd/gatherbot

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gatherbot /gatherbot
ENV GATHER_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["/gatherbot"]
