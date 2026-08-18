# Build from the repository root:
#   docker build --platform linux/amd64 -f build/serverless-probe/Dockerfile .
FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY build/serverless-probe/main.go ./main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/serverless-probe ./main.go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/serverless-probe /serverless-probe
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/serverless-probe"]
